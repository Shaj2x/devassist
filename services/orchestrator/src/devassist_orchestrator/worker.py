"""The orchestrator's event-driven worker.

- `job.created` consumers (ORCHESTRATOR_CONCURRENCY of them) run jobs and
  publish `job.completed`. A job already in a terminal state is skipped, so
  redelivered events are harmless; a job interrupted mid-run (crash,
  deploy) is simply run again when its event is redelivered.
- One `validation.completed` consumer hands sandbox results to whichever
  replica is waiting for them (see tools.sandbox.KafkaValidator).
"""

from __future__ import annotations

import asyncio
import logging
import uuid
from collections.abc import Coroutine
from typing import Any

from devassist_common.db import Job, JobStatus
from devassist_common.db.session import create_engine, create_session_factory
from devassist_common.events import (
    Envelope,
    EventType,
    JobCompleted,
    JobCreated,
    ValidationCompleted,
)
from devassist_common.kafka import EventConsumer, EventPublisher, PermanentError
from devassist_common.locks import LockNotAcquired
from redis.asyncio import Redis

from devassist_orchestrator.config import Settings
from devassist_orchestrator.runner import execute_job
from devassist_orchestrator.tools.sandbox import KafkaValidator, result_key

log = logging.getLogger(__name__)

TERMINAL = {
    JobStatus.AWAITING_REVIEW,
    JobStatus.APPROVED,
    JobStatus.REJECTED,
    JobStatus.PR_OPENED,
    JobStatus.FAILED,
    JobStatus.CANCELLED,
}
RESULT_TTL_SECONDS = 3600


class Worker:
    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.running = False

    async def run(self, stop: asyncio.Event) -> None:
        s = self.settings
        publisher = EventPublisher(s.kafka_bootstrap_servers, s.service_name)
        await publisher.start()
        self.redis = Redis.from_url(s.redis_url)
        engine = create_engine(s.database_url, pool_size=2)
        self.sessions = create_session_factory(engine)
        self.publisher = publisher
        group = f"{s.kafka_consumer_group_prefix}-orchestrator"
        consumers = [
            EventConsumer(
                s.kafka_bootstrap_servers,
                group,
                [EventType.JOB_CREATED],
                self.handle_job_created,
                publisher.producer,
                max_attempts=3,
            )
            for _ in range(s.orchestrator_concurrency)
        ]
        # Every replica must see every result, so this group is per process.
        results = EventConsumer(
            s.kafka_bootstrap_servers,
            f"{group}-results-{uuid.uuid4().hex[:8]}",
            [EventType.VALIDATION_COMPLETED],
            self.handle_validation_completed,
            publisher.producer,
            # The group is new on every start, and only results for patches
            # this process is waiting on matter: never replay the history.
            start_from="latest",
        )
        log.info("worker starting", extra={"concurrency": s.orchestrator_concurrency})
        try:
            # Listen for results before taking jobs, so no result for a patch
            # this process validates can arrive before the listener is in place.
            results_task = asyncio.create_task(results.run(stop))
            await _first(results.ready.wait(), results_task, within=120)
            self.running = True
            log.info("worker started")
            await asyncio.gather(results_task, *(c.run(stop) for c in consumers))
        finally:
            self.running = False
            await publisher.stop()
            await self.redis.aclose()
            await engine.dispose()
            log.info("worker stopped")

    async def handle_job_created(self, env: Envelope) -> None:
        payload = env.typed_payload()
        assert isinstance(payload, JobCreated)
        async with self.sessions() as s:
            job = await s.get(Job, payload.job_id)
        if job is None:
            raise PermanentError(f"job {payload.job_id} does not exist")
        if job.status in TERMINAL:
            log.info("job already finished; skipping event", extra={"status": job.status.value})
            return

        validator = KafkaValidator(
            self.publisher, self.redis, timeout=self.settings.sandbox_timeout_seconds
        )
        try:
            outcome = await execute_job(str(payload.job_id), self.settings, validator=validator)
        except LockNotAcquired:
            log.info("job is running on another worker; skipping")
            return

        await self.publisher.publish(
            EventType.JOB_COMPLETED,
            JobCompleted(
                job_id=payload.job_id,
                status="awaiting_review" if outcome.status == "awaiting_review" else "failed",
                final_patch_id=uuid.UUID(outcome.final_patch_id)
                if outcome.final_patch_id
                else None,
                iterations=outcome.iterations,
                error=outcome.error,
            ),
            trace_id=str(payload.job_id),
        )

    async def handle_validation_completed(self, env: Envelope) -> None:
        result = env.typed_payload()
        assert isinstance(result, ValidationCompleted)
        key = result_key(str(result.patch_id))
        await self.redis.rpush(key, result.model_dump_json())
        await self.redis.expire(key, RESULT_TTL_SECONDS)


async def _first(
    ready: Coroutine[Any, Any, Any], task: asyncio.Task[None], *, within: float
) -> None:
    """Wait for `ready`, but fail fast if `task` dies first."""
    waiter = asyncio.ensure_future(ready)
    done, _ = await asyncio.wait(
        {waiter, task}, timeout=within, return_when=asyncio.FIRST_COMPLETED
    )
    if waiter in done:
        return
    waiter.cancel()
    if task in done:
        task.result()  # re-raise the consumer's error
    raise TimeoutError(f"validation results consumer not ready after {within}s")
