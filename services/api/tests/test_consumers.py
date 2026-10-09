from __future__ import annotations

import json
import uuid
from typing import Any

from devassist_api.consumers import Notifier
from devassist_common.events import EventType, JobCompleted, RepoIndexed, new_event
from devassist_common.progress import NOTIFICATIONS_CHANNEL


class FakeRedis:
    def __init__(self) -> None:
        self.published: list[tuple[str, dict[str, Any]]] = []

    async def publish(self, channel: str, message: str) -> None:
        self.published.append((channel, json.loads(message)))


async def test_repo_indexed_is_relayed_as_a_notification() -> None:
    redis = FakeRedis()
    notifier = Notifier(sessions=None, redis=redis)  # type: ignore[arg-type]
    repo_id = uuid.uuid4()
    env = new_event(
        EventType.REPO_INDEXED,
        RepoIndexed(repo_id=repo_id, snapshot_id=uuid.uuid4(), commit_sha="abc", status="ready"),
        source="indexer",
    )
    await notifier.handle(env)
    assert redis.published == [
        (
            NOTIFICATIONS_CHANNEL,
            {"type": "repo.indexed", "repo_id": str(repo_id), "status": "ready"},
        )
    ]


async def test_job_completed_settles_then_notifies() -> None:
    redis = FakeRedis()
    settled: list[Any] = []

    class Recording(Notifier):
        async def _settle_job(self, env: Any, payload: JobCompleted) -> None:
            settled.append(payload.job_id)

    job_id = uuid.uuid4()
    env = new_event(
        EventType.JOB_COMPLETED,
        JobCompleted(job_id=job_id, status="failed", iterations=3, error="budget"),
        source="orchestrator",
    )
    await Recording(sessions=None, redis=redis).handle(env)  # type: ignore[arg-type]
    assert settled == [job_id]
    assert redis.published[0][1] == {
        "type": "job.completed",
        "job_id": str(job_id),
        "status": "failed",
    }
