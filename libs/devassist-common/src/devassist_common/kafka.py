"""Kafka for the Python services, mirroring libs/gocommon/kafkax.

- Topic == event type; messages are keyed by trace id so one job's events
  stay ordered on one partition.
- At-least-once: the offset is committed only after the handler succeeded
  or the message was dead-lettered. Handlers must be idempotent; use
  `claim_event` inside the handler's transaction.
- Failures are retried with exponential backoff; `PermanentError` (bad
  payload, unknown entity) and exhausted retries go to `<topic>.dlq` with
  the error in headers, and the consumer moves on.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
import uuid
from collections.abc import Awaitable, Callable
from typing import Any, Protocol

from aiokafka import AIOKafkaConsumer, AIOKafkaProducer
from sqlalchemy.dialects.postgresql import insert
from sqlalchemy.ext.asyncio import AsyncSession

from devassist_common.db import ProcessedEvent
from devassist_common.events import Envelope, EventType, dlq_topic, new_event
from devassist_common.events import _Payload as Payload
from devassist_common.logging import trace_context

log = logging.getLogger(__name__)

Handler = Callable[[Envelope], Awaitable[None]]


class PermanentError(Exception):
    """Retrying will not help; dead-letter the message immediately."""


class Producer(Protocol):
    async def send_and_wait(
        self,
        topic: str,
        value: bytes | None = None,
        key: bytes | None = None,
        headers: list[tuple[str, bytes]] | None = None,
    ) -> Any: ...


class EventPublisher:
    def __init__(
        self, bootstrap_servers: str, source: str, *, producer: Producer | None = None
    ) -> None:
        self.source = source
        self._own = producer is None
        self.producer: Any = producer or AIOKafkaProducer(
            bootstrap_servers=bootstrap_servers,
            acks="all",
            enable_idempotence=True,
            linger_ms=5,
        )

    async def start(self) -> None:
        if self._own:
            await self.producer.start()

    async def stop(self) -> None:
        if self._own:
            await self.producer.stop()

    async def publish(self, event_type: EventType, payload: Payload, *, trace_id: str) -> Envelope:
        env = new_event(event_type, payload, source=self.source, trace_id=trace_id)
        await self.producer.send_and_wait(
            event_type.value, value=env.to_bytes(), key=trace_id.encode()
        )
        log.info("event published", extra={"type": event_type.value, "event_id": str(env.event_id)})
        return env


class EventConsumer:
    def __init__(
        self,
        bootstrap_servers: str,
        group_id: str,
        topics: list[EventType],
        handler: Handler,
        dlq: Producer,
        *,
        max_attempts: int = 5,
        initial_backoff: float = 1.0,
        max_backoff: float = 30.0,
        max_poll_interval_ms: int = 30 * 60 * 1000,
    ) -> None:
        self.bootstrap_servers, self.group_id = bootstrap_servers, group_id
        self.topics = [t.value for t in topics]
        self.handler, self.dlq = handler, dlq
        self.max_attempts, self.initial_backoff, self.max_backoff = (
            max_attempts,
            initial_backoff,
            max_backoff,
        )
        # Handlers may run for minutes (a whole agent job); Kafka must not
        # assume the consumer died while one is running.
        self.max_poll_interval_ms = max_poll_interval_ms

    async def run(self, stop: asyncio.Event) -> None:
        consumer = AIOKafkaConsumer(
            *self.topics,
            bootstrap_servers=self.bootstrap_servers,
            group_id=self.group_id,
            enable_auto_commit=False,
            auto_offset_reset="earliest",
            max_poll_interval_ms=self.max_poll_interval_ms,
            max_poll_records=1,
        )
        await consumer.start()
        log.info("consumer started", extra={"group": self.group_id, "topics": self.topics})
        try:
            while not stop.is_set():
                getter = asyncio.ensure_future(consumer.getone())
                stopper = asyncio.ensure_future(stop.wait())
                done, _ = await asyncio.wait({getter, stopper}, return_when=asyncio.FIRST_COMPLETED)
                if getter not in done:
                    getter.cancel()
                    with contextlib.suppress(asyncio.CancelledError):
                        await getter
                    break
                stopper.cancel()
                msg = getter.result()
                await self.process(msg.topic, msg.value, msg.key, msg.offset)
                await consumer.commit()
        finally:
            await consumer.stop()

    async def process(
        self, topic: str, value: bytes | None, key: bytes | None, offset: int
    ) -> None:
        """Handle one message; never raises. Returns when it is safe to commit."""
        try:
            env = Envelope.from_bytes(value or b"")
        except Exception as e:
            log.error(
                "undecodable message; dead-lettering", extra={"topic": topic, "offset": offset}
            )
            await self._dead_letter(topic, value, key, offset, e, attempts=0)
            return
        with trace_context(env.trace_id):
            backoff = self.initial_backoff
            for attempt in range(1, self.max_attempts + 1):
                try:
                    await self.handler(env)
                    return
                except PermanentError as e:
                    log.error(
                        "permanent failure; dead-lettering",
                        extra={"type": env.type.value, "error": str(e)},
                    )
                    await self._dead_letter(topic, value, key, offset, e, attempts=attempt)
                    return
                except Exception as e:
                    if attempt == self.max_attempts:
                        log.exception(
                            "retries exhausted; dead-lettering", extra={"type": env.type.value}
                        )
                        await self._dead_letter(topic, value, key, offset, e, attempts=attempt)
                        return
                    log.warning(
                        "handler failed; retrying",
                        extra={
                            "type": env.type.value,
                            "attempt": attempt,
                            "backoff_s": backoff,
                            "error": str(e),
                        },
                    )
                    await asyncio.sleep(backoff)
                    backoff = min(backoff * 2, self.max_backoff)

    async def _dead_letter(
        self,
        topic: str,
        value: bytes | None,
        key: bytes | None,
        offset: int,
        error: Exception,
        *,
        attempts: int,
    ) -> None:
        headers = [
            ("x-error", str(error).encode()[:1000]),
            ("x-attempts", str(attempts).encode()),
            ("x-original-offset", str(offset).encode()),
        ]
        await self.dlq.send_and_wait(dlq_topic(topic), value=value, key=key, headers=headers)


async def claim_event(session: AsyncSession, consumer: str, env: Envelope) -> bool:
    """Record (consumer, event_id) in this transaction. Returns False if the
    event was already processed, in which case the caller should skip it.
    Commit the session together with the handler's own writes."""
    stmt = (
        insert(ProcessedEvent)
        .values(consumer=consumer, event_id=env.event_id, event_type=env.type.value)
        .on_conflict_do_nothing()
        .returning(ProcessedEvent.event_id)
    )
    claimed: uuid.UUID | None = await session.scalar(stmt)
    return claimed is not None
