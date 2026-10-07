"""The five agents: who they are, what they see, what they must return.

Context is assembled by the workflow, not stuffed in wholesale: agents get
the file tree, retrieval results for the task, and the full text of only the
files the plan names. Prompts state the contract plainly and leave judgment
to the model.
"""

from __future__ import annotations

from devassist_common.db import AgentName

from devassist_orchestrator.agents.base import Agent, AgentContext, render_json
from devassist_orchestrator.agents.outputs import CodeChange, DebugResult, Plan, Review

_EDIT_RULES = """\
Return edits as complete file contents: for each file you change, give its
full new text (not a diff, not a fragment). Paths are relative to the
repository root. Change only what the task needs; keep the existing style."""


def _tree(ctx: AgentContext, limit: int = 300) -> str:
    shown = ctx.file_tree[:limit]
    more = (
        f"\n... and {len(ctx.file_tree) - limit} more files" if len(ctx.file_tree) > limit else ""
    )
    return "\n".join(shown) + more


def _files(ctx: AgentContext) -> str:
    if not ctx.files:
        return "(none)"
    return "\n\n".join(f"### {path}\n```\n{content}\n```" for path, content in ctx.files.items())


def _section(title: str, body: str) -> str:
    return f"## {title}\n\n{body.strip() or '(none)'}\n"


# ---------------------------------------------------------------------------

PLANNER = Agent(
    name=AgentName.PLANNER,
    output=Plan,
    effort="high",
    system="""\
You are the planning agent in DevAssist, a system that turns a change request
into a validated patch for a code repository. Read the request, the
repository layout and the most relevant code, then write a precise plan for
the coding agent.

Name every source file that must change (or be created). Prefer the smallest
change that fully solves the request. List concrete risks, say how the change
should be tested, and suggest code searches that would give the coding agent
useful context (function names, identifiers, error messages).""",
    render=lambda ctx: "\n".join(
        [
            _section("Change request", ctx.task),
            _section(f"Repository {ctx.repo_name}: files", _tree(ctx)),
            _section("Relevant code (semantic search)", ctx.snippets),
        ]
    ),
)

CODER = Agent(
    name=AgentName.CODER,
    output=CodeChange,
    effort="high",
    system=f"""\
You are the coding agent in DevAssist. Implement the plan you are given in
the repository's source code. Do not write or modify tests; a separate
testing agent does that.

{_EDIT_RULES}""",
    render=lambda ctx: "\n".join(
        [
            _section("Change request", ctx.task),
            _section("Plan", render_json(ctx.plan)),
            _section("Current contents of the files to change", _files(ctx)),
            _section("Related code (semantic search)", ctx.snippets),
            _section("Repository files", _tree(ctx, 150)),
        ]
    ),
)

TESTER = Agent(
    name=AgentName.TESTER,
    output=CodeChange,
    effort="high",
    system=f"""\
You are the testing agent in DevAssist. Given a change request and the patch
that implements it, write or update tests that would fail without the patch
and pass with it, following the repository's existing test conventions. Edit
only test files. If existing tests already cover the change well, return no
edits and say so.

{_EDIT_RULES}""",
    render=lambda ctx: "\n".join(
        [
            _section("Change request", ctx.task),
            _section("Test strategy from the plan", str((ctx.plan or {}).get("test_strategy", ""))),
            _section("The patch", f"```diff\n{ctx.diff}\n```"),
            _section("Existing test files", _files(ctx)),
            _section("Repository files", _tree(ctx, 150)),
        ]
    ),
)

DEBUGGER = Agent(
    name=AgentName.DEBUGGER,
    output=DebugResult,
    effort="high",
    system=f"""\
You are the debugging agent in DevAssist. A patch was validated in a sandbox
(tests, security scan, static analysis) and failed. Find the root cause from
the validation report and fix it. The fix may touch source or tests, but
never weaken a correct test to make it pass: if a test encodes the intended
behaviour, fix the code. Earlier failed attempts are listed; do not repeat
them.

{_EDIT_RULES}""",
    render=lambda ctx: "\n".join(
        [
            _section("Change request", ctx.task),
            _section("Plan", render_json(ctx.plan)),
            _section("Current patch (against the original code)", f"```diff\n{ctx.diff}\n```"),
            _section("Validation report", ctx.validation),
            _section("Current contents of the changed and failing files", _files(ctx)),
            _section("Earlier attempts", "\n\n".join(ctx.history)),
        ]
    ),
)

REVIEWER = Agent(
    name=AgentName.REVIEWER,
    output=Review,
    effort="medium",
    system="""\
You are the review agent in DevAssist. A patch passed sandbox validation. Do
a final review for the human who will approve it: summarize what changed,
rate the risk, and flag anything they should check (behaviour changes,
missing edge cases, security, tests that may be too narrow). Be specific and
brief; the human sees the diff and the validation results next to your
review.""",
    render=lambda ctx: "\n".join(
        [
            _section("Change request", ctx.task),
            _section("Plan", render_json(ctx.plan)),
            _section("Final patch", f"```diff\n{ctx.diff}\n```"),
            _section("Validation results", ctx.validation),
        ]
    ),
)
