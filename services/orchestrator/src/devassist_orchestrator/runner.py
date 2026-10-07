"""Run one job end to end: load it, lock it, wire collaborators, execute.

`execute_job` is shared by the CLI and (Phase 5) the job.created consumer.
"""

from __future__ import annotations

import logging
import uuid
from decimal import Decimal

from devassist_common.db import Job, Repository
from devassist_common.db.session import create_engine, create_session_factory
from devassist_common.locks import redis_lock
from redis.asyncio import Redis
from sqlalchemy import select

from devassist_orchestrator.config import Settings
from devassist_orchestrator.llm import LLMProvider, create_provider
from devassist_orchestrator.store import SqlJobStore
from devassist_orchestrator.tools.retrieval import RetrievalTool
from devassist_orchestrator.tools.sandbox import HttpValidator, Validator
from devassist_orchestrator.workflow import JobRunner, JobSpec, Outcome

log = logging.getLogger(__name__)


async def execute_job(
    job_id: str,
    settings: Settings,
    *,
    llm: LLMProvider | None = None,
    validator: Validator | None = None,
) -> Outcome:
    engine = create_engine(settings.database_url, pool_size=2)
    sessions = create_session_factory(engine)
    redis = Redis.from_url(settings.redis_url)
    llm = llm or create_provider(settings)
    retrieval = RetrievalTool(settings.indexer_url)
    own_validator = validator is None
    validator = validator or HttpValidator(
        settings.sandbox_runner_url, timeout=settings.sandbox_timeout_seconds
    )
    try:
        async with sessions() as s:
            job = await s.get(Job, uuid.UUID(job_id))
            if job is None:
                raise ValueError(f"job {job_id} not found")
            repo = await s.scalar(select(Repository).where(Repository.id == job.repo_id))
            assert repo is not None
            spec = JobSpec(
                job_id=job_id,
                repo_id=str(repo.id),
                repo_name=repo.full_name,
                clone_url=repo.clone_url,
                default_branch=repo.default_branch,
                task=job.task,
                max_iterations=job.max_iterations,
                base_commit_sha=job.base_commit_sha,
                validation_config=dict(repo.config or {}),
            )
        store = SqlJobStore(sessions, redis, job_id, provider=llm.name, model=llm.model)
        runner = JobRunner(
            llm,
            store,
            retrieval,
            validator,
            work_dir=settings.work_dir,
            budget_usd=Decimal(str(settings.job_budget_usd)),
            top_k=settings.retrieval_top_k,
            max_file_chars=settings.max_file_chars,
            github_token=settings.github_token,
        )
        # Two workers must never run the same job.
        async with redis_lock(redis, f"job:{job_id}", ttl_seconds=120):
            return await runner.run(spec)
    finally:
        await llm.aclose()
        await retrieval.aclose()
        if own_validator and isinstance(validator, HttpValidator):
            await validator.aclose()
        await redis.aclose()
        await engine.dispose()
