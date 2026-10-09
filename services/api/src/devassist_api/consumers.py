"""The API's Kafka consumers.

The indexer and orchestrator own the database rows for snapshots and job
progress, so these consumers only relay coarse notifications to Redis
(`devassist:events`), where the dashboard's notification stream picks them
up. `job.completed` also repairs a job left in a running state, which can
happen if the orchestrator's final status write failed.
"""

from __future__ import annotations

import asyncio
import json
import logging

from devassist_common.db import Job, JobStatus
from devassist_common.events import Envelope, EventType, JobCompleted, RepoIndexed
from devassist_common.kafka import EventConsumer, Producer, claim_event
from devassist_common.progress import NOTIFICATIONS_CHANNEL
from redis.asyncio import Redis
from sqlalchemy import update
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

log = logging.getLogger(__name__)

CONSUMER_NAME = "api"
RUNNING = {
    JobStatus.QUEUED,
    JobStatus.PLANNING,
    JobStatus.CODING,
    JobStatus.TESTING,
    JobStatus.VALIDATING,
    JobStatus.DEBUGGING,
    JobStatus.REVIEWING,
}


class Notifier:
    def __init__(self, sessions: async_sessionmaker[AsyncSession], redis: Redis) -> None:
        self.sessions, self.redis = sessions, redis

    async def handle(self, env: Envelope) -> None:
        payload = env.typed_payload()
        if isinstance(payload, RepoIndexed):
            note = {
                "type": env.type.value,
                "repo_id": str(payload.repo_id),
                "status": payload.status,
            }
        elif isinstance(payload, JobCompleted):
            await self._settle_job(env, payload)
            note = {"type": env.type.value, "job_id": str(payload.job_id), "status": payload.status}
        else:
            return
        await self.redis.publish(NOTIFICATIONS_CHANNEL, json.dumps(note))

    async def _settle_job(self, env: Envelope, payload: JobCompleted) -> None:
        async with self.sessions() as s:
            if not await claim_event(s, CONSUMER_NAME, env):
                return
            await s.execute(
                update(Job)
                .where(Job.id == payload.job_id, Job.status.in_(RUNNING))
                .values(status=JobStatus(payload.status), error=payload.error)
            )
            await s.commit()


async def run_consumers(
    bootstrap_servers: str,
    group_prefix: str,
    notifier: Notifier,
    dlq: Producer,
    stop: asyncio.Event,
) -> None:
    consumer = EventConsumer(
        bootstrap_servers,
        f"{group_prefix}-api",
        [EventType.REPO_INDEXED, EventType.JOB_COMPLETED],
        notifier.handle,
        dlq,
    )
    await consumer.run(stop)
