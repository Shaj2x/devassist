from __future__ import annotations

import os
import subprocess
import uuid
from decimal import Decimal
from pathlib import Path
from typing import Any

import pytest
from devassist_common.db import AgentName, JobStatus
from devassist_common.events import CheckResult, ValidationCompleted
from devassist_orchestrator.llm import LLMRequest, LLMResponse
from devassist_orchestrator.tools.workspace import DiffStats


class MemoryStore:
    """In-memory JobStore for workflow tests."""

    def __init__(self) -> None:
        self.steps: list[dict[str, Any]] = []
        self.statuses: list[str] = []
        self.patches: list[dict[str, Any]] = []
        self.validations: list[tuple[str, str]] = []
        self.plan: dict[str, Any] | None = None
        self.review: dict[str, Any] | None = None
        self.error: str | None = None
        self.cost = Decimal(0)

    async def start_step(self, agent: AgentName, iteration: int, request: LLMRequest) -> str:
        self.steps.append({"agent": agent.value, "iteration": iteration, "request": request})
        return str(len(self.steps) - 1)

    async def finish_step(
        self,
        step_id: str,
        *,
        response: LLMResponse | None,
        parsed: dict[str, Any] | None,
        error: str | None,
    ) -> None:
        self.steps[int(step_id)].update(error=error, parsed=parsed)
        if response is not None:
            self.cost += response.cost_usd

    async def spent_usd(self) -> Decimal:
        return self.cost

    async def set_status(
        self, status: JobStatus, *, iteration: int | None = None, error: str | None = None
    ) -> None:
        self.statuses.append(status.value)
        if error:
            self.error = error

    async def save_plan(self, plan: dict[str, Any]) -> None:
        self.plan = plan

    async def save_patch(
        self, iteration: int, diff: str, stats: DiffStats, files: dict[str, str | None]
    ) -> str:
        self.patches.append({"iteration": iteration, "diff": diff, "stats": stats, "files": files})
        return f"patch-{iteration}"

    async def record_validation(self, patch_id: str, result: ValidationCompleted) -> None:
        self.validations.append((patch_id, result.status))

    async def save_review(self, review: dict[str, Any]) -> None:
        self.review = review

    def agents(self) -> list[str]:
        return [s["agent"] for s in self.steps]


def validation(status: str, failing: str = "") -> ValidationCompleted:
    tests = CheckResult(
        status="failed" if failing else "passed",
        passed=3,
        failed=1 if failing else 0,
        summary=failing or "3 passed",
    )
    clean = CheckResult(status="passed")
    return ValidationCompleted(
        job_id=str(uuid.uuid4()),
        patch_id=str(uuid.uuid4()),
        iteration=1,
        status=status,
        tests=tests,
        security=clean,
        static_analysis=clean,
    )


def git(cwd: Path, *args: str) -> str:
    env = {
        **os.environ,
        "GIT_AUTHOR_NAME": "t",
        "GIT_AUTHOR_EMAIL": "t@x",
        "GIT_COMMITTER_NAME": "t",
        "GIT_COMMITTER_EMAIL": "t@x",
    }
    result = subprocess.run(  # noqa: S603  (test helper, fixed binary)
        ["git", *args],  # noqa: S607
        cwd=cwd,
        env=env,
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout


@pytest.fixture
def repo_url(tmp_path: Path) -> str:
    """A tiny git repo: one module, one test file."""
    src = tmp_path / "src-repo"
    (src / "app").mkdir(parents=True)
    (src / "tests").mkdir()
    (src / "app" / "math.py").write_text("def add(a, b):\n    return a - b\n")
    (src / "tests" / "test_math.py").write_text(
        "from app.math import add\n\n\ndef test_add():\n    assert add(1, 1) == 2\n"
    )
    git(src, "init", "-q", "-b", "main")
    git(src, "add", ".")
    git(src, "commit", "-q", "-m", "init")
    return f"file://{src}"


@pytest.fixture
def store() -> MemoryStore:
    return MemoryStore()


@pytest.fixture
def make_validation() -> Any:
    return validation
