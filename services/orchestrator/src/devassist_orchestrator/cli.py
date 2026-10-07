"""`devassist-orchestrator run`: create a job and run it in the foreground,
then print the agent trace. Useful for demos and for iterating on prompts.

    devassist-orchestrator run --repo demo/datekit \\
        --task "Fix the failing test in datekit/calendar.py"
"""

from __future__ import annotations

import argparse
import asyncio
import uuid

from devassist_common.db import AgentStep, Job, JobStatus, Patch, Repository, ValidationRun
from devassist_common.db.session import create_engine, create_session_factory
from devassist_common.logging import configure_logging
from sqlalchemy import select

from devassist_orchestrator.config import Settings
from devassist_orchestrator.runner import execute_job


def add_run_parser(sub: argparse._SubParsersAction[argparse.ArgumentParser]) -> None:
    p = sub.add_parser("run", help="create and run one job in the foreground")
    p.add_argument("--repo", required=True, help="repository full name, e.g. demo/datekit")
    p.add_argument("--task", required=True, help="the change request, in plain English")
    p.add_argument("--max-iterations", type=int, default=None)
    p.add_argument(
        "--mock-script", default=None, help="mock LLM script name (sets LLM_PROVIDER=mock)"
    )
    p.add_argument("--show-diff", action="store_true", help="print the final patch")


async def run_command(args: argparse.Namespace) -> int:
    overrides = {}
    if args.mock_script:
        overrides = {"llm_provider": "mock", "llm_mock_script": args.mock_script}
    settings = Settings(**overrides)
    configure_logging(settings.service_name, settings.log_level)

    engine = create_engine(settings.database_url, pool_size=1)
    sessions = create_session_factory(engine)
    try:
        async with sessions() as s:
            repo = await s.scalar(select(Repository).where(Repository.full_name == args.repo))
            if repo is None:
                print(f"repository {args.repo!r} not found; index it first (make demo-index)")
                return 2
            job = Job(
                repo_id=repo.id,
                task=args.task,
                status=JobStatus.QUEUED,
                max_iterations=args.max_iterations or settings.max_iterations,
            )
            s.add(job)
            await s.commit()
            job_id = str(job.id)
        print(f"job {job_id}: {args.task}\n")

        outcome = await execute_job(job_id, settings)
        await _print_trace(sessions, job_id, show_diff=args.show_diff)
        print(
            f"\nresult: {outcome.status.upper()} after {outcome.iterations} iteration(s)"
            + (f" - {outcome.error}" if outcome.error else "")
        )
        return 0 if outcome.status == "awaiting_review" else 1
    finally:
        await engine.dispose()


async def _print_trace(sessions, job_id: str, *, show_diff: bool) -> None:  # type: ignore[no-untyped-def]
    jid = uuid.UUID(job_id)
    async with sessions() as s:
        steps = (
            await s.scalars(
                select(AgentStep).where(AgentStep.job_id == jid).order_by(AgentStep.sequence)
            )
        ).all()
        patches = (
            await s.scalars(select(Patch).where(Patch.job_id == jid).order_by(Patch.iteration))
        ).all()
        runs = {
            r.patch_id: r
            for r in (
                await s.scalars(
                    select(ValidationRun).where(ValidationRun.patch_id.in_([p.id for p in patches]))
                )
            ).all()
        }
        job = await s.get(Job, jid)
    assert job is not None

    row = "{:>2}  {:>4}  {:<9} {:<9} {:>7} {:>7} {:>9} {:>6}"
    print(row.format("#", "iter", "agent", "status", "in_tok", "out_tok", "cost $", "ms"))
    for st in steps:
        print(
            row.format(
                st.sequence,
                st.iteration,
                st.agent.value,
                st.status.value,
                st.input_tokens,
                st.output_tokens,
                f"{st.cost_usd:.4f}",
                st.latency_ms or 0,
            )
        )
    print(
        row.format(
            "",
            "",
            "total",
            "",
            job.total_input_tokens,
            job.total_output_tokens,
            f"{job.total_cost_usd:.4f}",
            "",
        )
    )

    print("\npatches:")
    for p in patches:
        r = runs.get(p.id)
        verdict = (
            (
                f"tests={r.tests_status.value if r.tests_status else '-'} "
                f"security={r.security_status.value if r.security_status else '-'} "
                f"static={r.static_status.value if r.static_status else '-'}"
            )
            if r
            else "not validated"
        )
        print(
            f"  iteration {p.iteration}: {p.status.value:<10} +{p.additions}/-{p.deletions} "
            f"in {p.files_changed} file(s)  [{verdict}]"
        )
    if job.review:
        print(f"\nreview ({job.review.get('risk_level')} risk): {job.review.get('summary')}")
        for c in job.review.get("concerns", []):
            print(f"  - {c}")
    if show_diff and patches:
        print(f"\nfinal patch:\n{patches[-1].diff}")


def run(args: argparse.Namespace) -> int:
    return asyncio.run(run_command(args))
