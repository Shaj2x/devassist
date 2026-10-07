"""The agent loop, as an explicit state machine.

    PLAN -> CODE -> TEST -> VALIDATE --passed--> REVIEW -> DONE
                               |  ^
                         failed|  |new patch
                               v  |
                              DEBUG

Each state is one method that does its work and returns the next state, so
the whole control flow is readable in `run`. The loop cannot run forever:

- at most `max_iterations` patches are validated per job;
- a patch identical to an earlier one ends the job ("stuck"): the debugger
  is going in circles and more iterations would only cost money;
- every agent call checks the job's cost budget first;
- every LLM call has a timeout and a bounded retry count.
"""

from __future__ import annotations

import logging
import re
from dataclasses import dataclass, field
from decimal import Decimal
from enum import StrEnum
from typing import Any, Protocol

import httpx
from devassist_common.db import JobStatus
from devassist_common.events import ValidationCompleted
from devassist_common.logging import trace_context

from devassist_orchestrator.agents import (
    CODER,
    DEBUGGER,
    PLANNER,
    REVIEWER,
    TESTER,
    AgentContext,
    BudgetExceeded,
    StepRecorder,
)
from devassist_orchestrator.agents.outputs import Plan
from devassist_orchestrator.llm import LLMError, LLMProvider
from devassist_orchestrator.tools.retrieval import CodeHit, Retriever
from devassist_orchestrator.tools.sandbox import ValidationRequest, Validator
from devassist_orchestrator.tools.workspace import (
    DiffStats,
    FileEdit,
    Workspace,
    WorkspaceError,
    diff_fingerprint,
)

log = logging.getLogger(__name__)


class State(StrEnum):
    PLAN = "plan"
    CODE = "code"
    TEST = "test"
    VALIDATE = "validate"
    DEBUG = "debug"
    REVIEW = "review"
    DONE = "done"
    FAILED = "failed"


class JobStore(StepRecorder, Protocol):
    """Persistence the workflow needs (SQL in production, in-memory in tests)."""

    async def set_status(
        self, status: JobStatus, *, iteration: int | None = None, error: str | None = None
    ) -> None: ...

    async def save_plan(self, plan: dict[str, Any]) -> None: ...

    async def save_patch(self, iteration: int, diff: str, stats: DiffStats) -> str: ...

    async def record_validation(self, patch_id: str, result: ValidationCompleted) -> None: ...

    async def save_review(self, review: dict[str, Any]) -> None: ...


@dataclass(frozen=True)
class JobSpec:
    job_id: str
    repo_id: str
    repo_name: str
    clone_url: str
    default_branch: str
    task: str
    max_iterations: int
    base_commit_sha: str | None = None
    validation_config: dict[str, Any] = field(default_factory=dict)


@dataclass
class Outcome:
    status: str  # "awaiting_review" | "failed"
    iterations: int
    final_patch_id: str | None = None
    error: str | None = None


@dataclass
class _Run:
    """Mutable state of one job run."""

    spec: JobSpec
    ws: Workspace
    ctx: AgentContext
    plan: Plan | None = None
    patch_id: str | None = None
    validation: ValidationCompleted | None = None
    seen: set[str] = field(default_factory=set)
    error: str | None = None


class JobRunner:
    def __init__(
        self,
        llm: LLMProvider,
        store: JobStore,
        retrieval: Retriever,
        validator: Validator,
        *,
        work_dir: str,
        budget_usd: Decimal,
        top_k: int = 8,
        max_file_chars: int = 60_000,
        github_token: str = "",
    ) -> None:
        self.llm, self.store, self.retrieval, self.validator = llm, store, retrieval, validator
        self.work_dir, self.budget, self.top_k = work_dir, budget_usd, top_k
        self.max_file_chars, self.github_token = max_file_chars, github_token

    async def run(self, spec: JobSpec) -> Outcome:
        with trace_context(spec.job_id):
            try:
                ws = await Workspace.checkout(
                    spec.clone_url,
                    work_dir=self.work_dir,
                    commit_sha=spec.base_commit_sha,
                    branch=spec.default_branch,
                    github_token=self.github_token,
                )
            except WorkspaceError as e:
                await self.store.set_status(JobStatus.FAILED, error=f"checkout failed: {e}")
                return Outcome("failed", 0, error=str(e))
            try:
                ctx = AgentContext(
                    task=spec.task, repo_name=spec.repo_name, file_tree=await ws.files()
                )
                run = _Run(spec=spec, ws=ws, ctx=ctx)
                return await self._loop(run)
            finally:
                ws.cleanup()

    async def _loop(self, run: _Run) -> Outcome:
        handlers = {
            State.PLAN: self._plan,
            State.CODE: self._code,
            State.TEST: self._test,
            State.VALIDATE: self._validate,
            State.DEBUG: self._debug,
            State.REVIEW: self._review,
        }
        state = State.PLAN
        while state not in (State.DONE, State.FAILED):
            log.info("entering state", extra={"state": state.value, "iteration": run.ctx.iteration})
            try:
                state = await handlers[state](run)
            except BudgetExceeded as e:
                run.error, state = str(e), State.FAILED
            except LLMError as e:
                run.error, state = f"{state.value} failed: {e}", State.FAILED
            except (WorkspaceError, httpx.HTTPError) as e:
                run.error, state = f"{state.value} failed: {type(e).__name__}: {e}", State.FAILED

        if state == State.DONE:
            await self.store.set_status(JobStatus.AWAITING_REVIEW, iteration=run.ctx.iteration)
            return Outcome("awaiting_review", run.ctx.iteration, final_patch_id=run.patch_id)
        await self.store.set_status(JobStatus.FAILED, iteration=run.ctx.iteration, error=run.error)
        log.warning("job failed", extra={"error": run.error})
        return Outcome("failed", run.ctx.iteration, final_patch_id=run.patch_id, error=run.error)

    # --- states ---------------------------------------------------------------

    async def _plan(self, run: _Run) -> State:
        await self.store.set_status(JobStatus.PLANNING, iteration=1)
        run.ctx.snippets = _render_hits(
            await self.retrieval.search(run.spec.repo_id, run.spec.task, self.top_k)
        )
        run.plan = await PLANNER.run(self.llm, self.store, run.ctx, budget_usd=self.budget)
        run.ctx.plan = run.plan.model_dump()
        await self.store.save_plan(run.ctx.plan)
        return State.CODE

    async def _code(self, run: _Run) -> State:
        assert run.plan is not None
        await self.store.set_status(JobStatus.CODING)
        queries = run.plan.search_queries or [run.spec.task]
        run.ctx.snippets = _render_hits(
            await self.retrieval.search_many(run.spec.repo_id, queries, self.top_k)
        )
        run.ctx.files = self._read([f.path for f in run.plan.files_to_change], run.ws)
        change = await CODER.run(self.llm, self.store, run.ctx, budget_usd=self.budget)
        run.ws.apply(change.edits)
        return State.TEST

    async def _test(self, run: _Run) -> State:
        await self.store.set_status(JobStatus.TESTING)
        run.ctx.diff = await run.ws.diff()
        changed = _changed_paths(run.ctx.diff)
        run.ctx.files = self._read(_related_tests(run.ctx.file_tree, changed), run.ws)
        change = await TESTER.run(self.llm, self.store, run.ctx, budget_usd=self.budget)
        tests_only = [e for e in change.edits if _is_test_path(e.path)]
        if dropped := [e.path for e in change.edits if not _is_test_path(e.path)]:
            log.warning("tester edited non-test files; ignored", extra={"paths": dropped})
        run.ws.apply(tests_only)
        return await self._new_patch(run)

    async def _validate(self, run: _Run) -> State:
        assert run.patch_id is not None
        await self.store.set_status(JobStatus.VALIDATING)
        result = await self.validator.validate(
            ValidationRequest(
                job_id=run.spec.job_id,
                patch_id=run.patch_id,
                iteration=run.ctx.iteration,
                clone_url=run.spec.clone_url,
                commit_sha=run.ws.commit_sha,
                diff=run.ctx.diff,
                config=run.spec.validation_config,
            )
        )
        run.validation = result
        await self.store.record_validation(run.patch_id, result)
        if result.status == "passed":
            return State.REVIEW
        if run.ctx.iteration >= run.spec.max_iterations:
            run.error = f"validation still {result.status} after {run.ctx.iteration} iteration(s)"
            return State.FAILED
        return State.DEBUG

    async def _debug(self, run: _Run) -> State:
        assert run.validation is not None
        run.ctx.iteration += 1
        await self.store.set_status(JobStatus.DEBUGGING, iteration=run.ctx.iteration)
        run.ctx.validation = render_validation(run.validation)
        paths = _changed_paths(run.ctx.diff) + _finding_files(run.validation)
        run.ctx.files = self._read(list(dict.fromkeys(paths)), run.ws)
        result = await DEBUGGER.run(self.llm, self.store, run.ctx, budget_usd=self.budget)
        run.ctx.history.append(
            f"Attempt {run.ctx.iteration - 1}: validation {run.validation.status}. "
            f"Diagnosis: {result.diagnosis}"
        )
        run.ws.apply(result.edits)
        return await self._new_patch(run)

    async def _review(self, run: _Run) -> State:
        assert run.validation is not None
        await self.store.set_status(JobStatus.REVIEWING)
        run.ctx.validation = render_validation(run.validation)
        review = await REVIEWER.run(self.llm, self.store, run.ctx, budget_usd=self.budget)
        await self.store.save_review(review.model_dump())
        return State.DONE

    # --- helpers --------------------------------------------------------------

    async def _new_patch(self, run: _Run) -> State:
        """Snapshot the workspace as the next patch, unless it is empty or a
        repeat of an earlier attempt."""
        diff = await run.ws.diff()
        if not diff.strip():
            run.error = "the agents produced no changes"
            return State.FAILED
        fingerprint = diff_fingerprint(diff)
        if fingerprint in run.seen:
            run.error = "stuck: the new patch is identical to an earlier attempt"
            return State.FAILED
        run.seen.add(fingerprint)
        run.ctx.diff = diff
        run.patch_id = await self.store.save_patch(run.ctx.iteration, diff, await run.ws.stats())
        return State.VALIDATE

    def _read(self, paths: list[str], ws: Workspace) -> dict[str, str]:
        files: dict[str, str] = {}
        for path in paths:
            try:
                content = ws.read(path, self.max_file_chars)
            except WorkspaceError:
                continue
            if content is None and ws.exists(path):
                continue  # binary
            files[path] = content if content is not None else "(file does not exist yet)"
        return files


# ---------------------------------------------------------------------------
# Rendering and path heuristics (pure functions, unit tested)
# ---------------------------------------------------------------------------


def _render_hits(hits: list[CodeHit]) -> str:
    return "\n\n".join(h.render() for h in hits)


_DIFF_FILE = re.compile(r"^\+\+\+ b/(.+)$", re.MULTILINE)
_DIFF_DELETED = re.compile(r"^--- a/(.+)\n\+\+\+ /dev/null$", re.MULTILINE)


def _changed_paths(diff: str) -> list[str]:
    return _DIFF_FILE.findall(diff) + _DIFF_DELETED.findall(diff)


_TEST_SUFFIXES = (
    "_test.py",
    "_test.go",
    ".test.ts",
    ".test.tsx",
    ".test.js",
    ".spec.ts",
    ".spec.js",
)
_SOURCE_EXTENSIONS = (".py", ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs")


def _is_test_path(path: str) -> bool:
    parts = path.split("/")
    name = parts[-1]
    if not name.endswith(_SOURCE_EXTENSIONS):
        return False
    return (
        any(p in ("tests", "test", "__tests__", "spec") for p in parts[:-1])
        or name.startswith("test_")
        or name.endswith(_TEST_SUFFIXES)
    )


def _related_tests(tree: list[str], changed: list[str], limit: int = 4) -> list[str]:
    """Existing test files most likely to cover the changed files: those
    whose name mentions a changed module, then any others in the repo."""
    tests = [p for p in tree if _is_test_path(p)]
    stems = {p.rsplit("/", 1)[-1].rsplit(".", 1)[0] for p in changed}
    related = [t for t in tests if any(stem and stem in t for stem in stems)]
    return list(dict.fromkeys(related + tests))[:limit]


def _finding_files(result: ValidationCompleted) -> list[str]:
    files = []
    for check in (result.tests, result.security, result.static_analysis):
        for f in check.findings:
            if f.file:
                files.append(f.file)
            elif f.rule_id and "::" in f.rule_id:  # pytest node id: tests.test_x::test_y
                files.append(f.rule_id.split("::")[0].replace(".", "/") + ".py")
    return files


def render_validation(r: ValidationCompleted, max_log: int = 3000) -> str:
    lines = [f"Overall: {r.status.upper()}"]
    if r.error:
        lines.append(f"Error: {r.error}")
    for name, check in (
        ("Tests", r.tests),
        ("Security", r.security),
        ("Static analysis", r.static_analysis),
    ):
        lines.append(f"\n{name}: {check.status} ({check.summary or ''})")
        for f in check.findings[:20]:
            loc = f"{f.file}:{f.line}" if f.file else ""
            lines.append(f"- [{f.severity or ''}] {f.rule_id or ''} {loc}\n  {f.message.strip()}")
        if check.status in ("failed", "error") and check.log:
            lines.append(f"Log tail:\n```\n{check.log[-max_log:]}\n```")
    return "\n".join(lines)


__all__ = ["FileEdit", "JobRunner", "JobSpec", "JobStore", "Outcome", "State", "render_validation"]
