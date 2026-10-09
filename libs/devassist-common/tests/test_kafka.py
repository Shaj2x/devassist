from __future__ import annotations

import uuid
from typing import Any

from devassist_common.events import EventType, JobCreated, new_event
from devassist_common.kafka import EventConsumer, EventPublisher, PermanentError
from devassist_common.logging import get_trace_id


class FakeProducer:
    def __init__(self) -> None:
        self.sent: list[dict[str, Any]] = []

    async def send_and_wait(
        self,
        topic: str,
        value: bytes | None = None,
        key: bytes | None = None,
        headers: list[tuple[str, bytes]] | None = None,
    ) -> None:
        self.sent.append(
            {"topic": topic, "value": value, "key": key, "headers": dict(headers or [])}
        )


def job_event() -> bytes:
    payload = JobCreated(job_id=uuid.uuid4(), repo_id=uuid.uuid4(), task="t", max_iterations=3)
    return new_event(
        EventType.JOB_CREATED, payload, source="test", trace_id=str(payload.job_id)
    ).to_bytes()


def consumer(handler: Any, dlq: FakeProducer, attempts: int = 3) -> EventConsumer:
    return EventConsumer(
        "unused:9092",
        "g",
        [EventType.JOB_CREATED],
        handler,
        dlq,
        max_attempts=attempts,
        initial_backoff=0.001,
    )


async def test_publish_routes_by_type_and_keys_by_trace() -> None:
    fake = FakeProducer()
    pub = EventPublisher("unused", "api", producer=fake)
    payload = JobCreated(job_id=uuid.uuid4(), repo_id=uuid.uuid4(), task="t", max_iterations=2)
    env = await pub.publish(EventType.JOB_CREATED, payload, trace_id="job-9")
    assert fake.sent[0]["topic"] == "job.created" and fake.sent[0]["key"] == b"job-9"
    assert env.source == "api" and env.trace_id == "job-9"


async def test_success_runs_handler_with_trace_context() -> None:
    seen: list[str | None] = []

    async def handler(env: Any) -> None:
        seen.append(get_trace_id())

    dlq = FakeProducer()
    await consumer(handler, dlq).process("job.created", job_event(), b"k", 1)
    assert seen and seen[0] is not None and not dlq.sent


async def test_transient_failures_retry_then_succeed() -> None:
    calls = 0

    async def flaky(env: Any) -> None:
        nonlocal calls
        calls += 1
        if calls < 3:
            raise ConnectionError("db busy")

    dlq = FakeProducer()
    await consumer(flaky, dlq).process("job.created", job_event(), b"k", 1)
    assert calls == 3 and not dlq.sent


async def test_exhausted_and_permanent_failures_are_dead_lettered() -> None:
    async def broken(env: Any) -> None:
        raise RuntimeError("still broken")

    dlq = FakeProducer()
    await consumer(broken, dlq, attempts=2).process("job.created", job_event(), b"k", 7)
    msg = dlq.sent[0]
    assert msg["topic"] == "job.created.dlq"
    assert msg["headers"] == {
        "x-error": b"still broken",
        "x-attempts": b"2",
        "x-original-offset": b"7",
    }

    calls = 0

    async def permanent(env: Any) -> None:
        nonlocal calls
        calls += 1
        raise PermanentError("unknown repo")

    await consumer(permanent, dlq).process("job.created", job_event(), b"k", 8)
    assert calls == 1 and len(dlq.sent) == 2

    await consumer(permanent, dlq).process("job.created", b"not json", b"k", 9)
    assert len(dlq.sent) == 3 and dlq.sent[2]["headers"]["x-attempts"] == b"0"
