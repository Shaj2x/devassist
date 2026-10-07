"""End-to-end: the full pipeline on the datekit sample with the LLM mocked.

Runs `devassist-orchestrator run` inside the running stack (make up), so the
real indexer, sandbox runner, Postgres and Redis are all involved; only the
model is scripted. Then checks what was persisted.
"""

from __future__ import annotations

import os
import re
import subprocess
from pathlib import Path

import pytest
from devassist_common.config import to_asyncpg_url
from sqlalchemy import text
from sqlalchemy.ext.asyncio import create_async_engine

pytestmark = pytest.mark.integration

ROOT = Path(__file__).resolve().parents[3]
COMPOSE = [
    "docker",
    "compose",
    "-f",
    str(ROOT / "deploy/docker-compose.yml"),
    "--env-file",
    str(ROOT / ".env"),
]
DATABASE_URL = os.environ.get(
    "DATABASE_URL", "postgresql://devassist:devassist@localhost:5432/devassist"
)


def compose(*args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run([*COMPOSE, *args], capture_output=True, text=True, timeout=600)  # noqa: S603


async def test_datekit_job_runs_plan_code_test_validate_debug_review() -> None:
    indexed = compose(
        "exec",
        "-T",
        "indexer",
        "indexer",
        "index",
        "-url",
        "file:///sample-repos/datekit.git",
        "-name",
        "demo/datekit",
    )
    assert indexed.returncode == 0, indexed.stderr

    run = compose(
        "exec",
        "-T",
        "-e",
        "LLM_PROVIDER=mock",
        "orchestrator",
        "devassist-orchestrator",
        "run",
        "--repo",
        "demo/datekit",
        "--task",
        "Fix the failing test in datekit/calendar.py",
        "--mock-script",
        "datekit_fix_leap_year",
    )
    assert run.returncode == 0, run.stdout + run.stderr
    match = re.search(r"^job ([0-9a-f-]{36})", run.stdout, re.MULTILINE)
    assert match, run.stdout
    job_id = match.group(1)

    engine = create_async_engine(to_asyncpg_url(DATABASE_URL))
    try:
        async with engine.connect() as conn:
            job = (
                await conn.execute(
                    text(
                        "SELECT status, current_iteration, review->>'risk_level', plan->>'summary' "
                        "FROM jobs WHERE id = :id"
                    ),
                    {"id": job_id},
                )
            ).one()
            steps = (
                await conn.execute(
                    text(
                        "SELECT agent, iteration, status FROM agent_steps "
                        "WHERE job_id = :id ORDER BY sequence"
                    ),
                    {"id": job_id},
                )
            ).all()
            patches = (
                await conn.execute(
                    text(
                        "SELECT p.iteration, p.status, v.status, v.tests->>'failed' FROM patches p "
                        "JOIN validation_runs v ON v.patch_id = p.id "
                        "WHERE p.job_id = :id ORDER BY p.iteration"
                    ),
                    {"id": job_id},
                )
            ).all()
    finally:
        await engine.dispose()

    assert job[0] == "awaiting_review" and job[1] == 2 and job[2] == "low" and job[3]
    assert [(s.agent, s.iteration) for s in steps] == [
        ("planner", 1),
        ("coder", 1),
        ("tester", 1),
        ("debugger", 2),
        ("reviewer", 2),
    ]
    assert all(s.status == "succeeded" for s in steps)
    # Iteration 1 really failed in the sandbox (3 broken tests); iteration 2 really passed.
    assert [(p[0], p[1], p[2], p[3]) for p in patches] == [
        (1, "superseded", "failed", "3"),
        (2, "passed", "passed", "0"),
    ]
