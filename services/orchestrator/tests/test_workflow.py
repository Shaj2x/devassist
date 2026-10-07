"""The state machine, end to end, with a scripted LLM and a fake sandbox."""

from __future__ import annotations

import copy
from collections.abc import Callable
from decimal import Decimal
from pathlib import Path
from typing import Any

from devassist_common.events import ValidationCompleted
from devassist_orchestrator.llm import LLMRequest, LLMResponse
from devassist_orchestrator.llm.mock_provider import MockProvider
from devassist_orchestrator.tools.retrieval import CodeHit
from devassist_orchestrator.tools.sandbox import ValidationRequest
from devassist_orchestrator.workflow import (
    JobRunner,
    JobSpec,
    _changed_paths,
    _is_test_path,
    _related_tests,
    render_validation,
)

FIXED = "def add(a, b):\n    return a + b\n"
WRONG = "def add(a, b):\n    return a * b\n"


def edit(path: str, content: str) -> dict[str, str]:
    return {"path": path, "action": "write", "content": content}


PLAN = {
    "summary": "fix add",
    "files_to_change": [{"path": "app/math.py", "change": "use +"}],
    "steps": ["fix"],
    "risks": [],
    "test_strategy": "test add",
    "search_queries": ["add"],
}
TESTS = {
    "edits": [
        edit(
            "tests/test_math.py",
            "from app.math import add\n\n\n"
            "def test_add():\n    assert add(1, 1) == 2\n    assert add(2, 3) == 5\n",
        )
    ],
    "explanation": "more cases",
}
REVIEW = {
    "summary": "fixes add",
    "risk_level": "low",
    "concerns": [],
    "suggested_followups": [],
    "ready_to_merge": True,
}


def script(coder_content: str, debugger_contents: list[str]) -> dict[str, list[dict[str, Any]]]:
    return {
        "planner": [PLAN],
        "coder": [{"edits": [edit("app/math.py", coder_content)], "explanation": "e"}],
        "tester": [TESTS],
        "debugger": [
            {"diagnosis": "d", "edits": [edit("app/math.py", c)], "explanation": "e"}
            for c in debugger_contents
        ],
        "reviewer": [REVIEW],
    }


class FakeRetrieval:
    async def search(self, repo_id: str, query: str, top_k: int = 8) -> list[CodeHit]:
        return []

    async def search_many(self, repo_id: str, queries: list[str], top_k: int = 8) -> list[CodeHit]:
        return []


class ScriptedSandbox:
    """Passes a patch iff app/math.py ends up using `+`."""

    def __init__(self, make_validation: Callable[..., ValidationCompleted]) -> None:
        self.make_validation = make_validation
        self.requests: list[ValidationRequest] = []

    async def validate(self, req: ValidationRequest) -> ValidationCompleted:
        self.requests.append(req)
        if "+    return a + b" in req.diff:
            return self.make_validation("passed")
        return self.make_validation("failed", failing="tests.test_math::test_add assert 1 == 2")


def runner(llm: Any, store: Any, sandbox: Any, tmp_path: Path, budget: str = "5") -> JobRunner:
    return JobRunner(
        llm,
        store,
        FakeRetrieval(),
        sandbox,
        work_dir=str(tmp_path / "w"),
        budget_usd=Decimal(budget),
    )


def spec(repo_url: str, max_iterations: int = 3) -> JobSpec:
    return JobSpec(
        job_id="job-1",
        repo_id="repo-1",
        repo_name="o/r",
        clone_url=repo_url,
        default_branch="main",
        task="add() subtracts; fix it",
        max_iterations=max_iterations,
    )


async def test_passes_first_time(
    repo_url: str, tmp_path: Path, store: Any, make_validation: Any
) -> None:
    sandbox = ScriptedSandbox(make_validation)
    out = await runner(MockProvider(script(FIXED, [])), store, sandbox, tmp_path).run(
        spec(repo_url)
    )

    assert out.status == "awaiting_review" and out.iterations == 1
    assert store.agents() == ["planner", "coder", "tester", "reviewer"]
    assert store.statuses == [
        "planning",
        "coding",
        "testing",
        "validating",
        "reviewing",
        "awaiting_review",
    ]
    assert len(store.patches) == 1 and store.review == REVIEW and store.plan == PLAN
    assert "tests/test_math.py" in store.patches[0]["diff"]


async def test_debug_loop_recovers(
    repo_url: str, tmp_path: Path, store: Any, make_validation: Any
) -> None:
    sandbox = ScriptedSandbox(make_validation)
    out = await runner(MockProvider(script(WRONG, [FIXED])), store, sandbox, tmp_path).run(
        spec(repo_url)
    )

    assert out.status == "awaiting_review" and out.iterations == 2
    assert store.agents() == ["planner", "coder", "tester", "debugger", "reviewer"]
    assert [v[1] for v in store.validations] == ["failed", "passed"]
    debug_prompt = store.steps[3]["request"].messages[0].content
    assert (
        "test_add" in debug_prompt and "a * b" in debug_prompt
    )  # sees the failure and the current patch


async def test_stops_at_max_iterations(
    repo_url: str, tmp_path: Path, store: Any, make_validation: Any
) -> None:
    sandbox = ScriptedSandbox(make_validation)
    llm = MockProvider(
        script(WRONG, ["def add(a, b):\n    return a - 0\n", "def add(a, b):\n    return b\n"])
    )
    out = await runner(llm, store, sandbox, tmp_path).run(spec(repo_url, max_iterations=2))

    assert out.status == "failed" and "after 2 iteration" in (out.error or "")
    assert len(sandbox.requests) == 2 and store.agents().count("debugger") == 1
    assert "reviewer" not in store.agents()


async def test_identical_patch_is_detected_as_stuck(
    repo_url: str, tmp_path: Path, store: Any, make_validation: Any
) -> None:
    sandbox = ScriptedSandbox(make_validation)
    llm = MockProvider(script(WRONG, [WRONG]))  # debugger "fixes" it to the same thing
    out = await runner(llm, store, sandbox, tmp_path).run(spec(repo_url, max_iterations=5))

    assert out.status == "failed" and "stuck" in (out.error or "")
    assert len(sandbox.requests) == 1  # the repeat was never re-validated


async def test_budget_stops_the_loop(
    repo_url: str, tmp_path: Path, store: Any, make_validation: Any
) -> None:
    class Pricey(MockProvider):
        async def complete(self, request: LLMRequest) -> LLMResponse:
            resp = await super().complete(request)
            resp.cost_usd = Decimal("0.40")
            return resp

    out = await runner(
        Pricey(script(WRONG, [FIXED])),
        store,
        ScriptedSandbox(make_validation),
        tmp_path,
        budget="1.00",
    ).run(spec(repo_url))
    assert out.status == "failed" and "budget" in (out.error or "")
    assert store.agents() == ["planner", "coder", "tester"]  # 3 x $0.40 crosses $1.00


async def test_no_changes_fails_cleanly(
    repo_url: str, tmp_path: Path, store: Any, make_validation: Any
) -> None:
    s = script(FIXED, [])
    s["coder"][0]["edits"] = []
    s["tester"] = [{"edits": [], "explanation": "nothing"}]
    out = await runner(
        MockProvider(copy.deepcopy(s)), store, ScriptedSandbox(make_validation), tmp_path
    ).run(spec(repo_url))
    assert out.status == "failed" and "no changes" in (out.error or "")


async def test_llm_failure_marks_job_failed(
    repo_url: str, tmp_path: Path, store: Any, make_validation: Any
) -> None:
    out = await runner(MockProvider({}), store, ScriptedSandbox(make_validation), tmp_path).run(
        spec(repo_url)
    )
    assert out.status == "failed" and "plan failed" in (out.error or "")
    assert store.statuses[-1] == "failed"


def test_helpers(make_validation: Any) -> None:
    diff = "diff --git a/x.py b/x.py\n--- a/x.py\n+++ b/x.py\n@@\n--- a/old.py\n+++ /dev/null\n"
    assert _changed_paths(diff) == ["x.py", "old.py"]
    assert (
        _is_test_path("tests/test_a.py")
        and _is_test_path("pkg/a_test.go")
        and _is_test_path("src/a.test.ts")
    )
    assert not _is_test_path("tests/__pycache__/test_a.cpython-311.pyc") and not _is_test_path(
        "app/math.py"
    )
    tree = ["app/calendar.py", "tests/test_calendar.py", "tests/test_other.py", "tests/conftest.py"]
    assert _related_tests(tree, ["app/calendar.py"])[0] == "tests/test_calendar.py"
    report = render_validation(make_validation("failed", failing="boom"))
    assert "Overall: FAILED" in report and "Tests: failed" in report
