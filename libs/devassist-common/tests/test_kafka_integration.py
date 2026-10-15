"""Kafka consumer behaviour against the real broker from `make up`."""

from __future__ import annotations

import asyncio
import os
import time
import uuid

import pytest
from aiokafka import AIOKafkaProducer
from aiokafka.admin import AIOKafkaAdminClient, NewTopic
from devassist_common.events import Envelope, EventType, RepoIndexed, new_event
from devassist_common.kafka import EventConsumer

pytestmark = pytest.mark.integration

BOOTSTRAP = os.environ.get("KAFKA_BOOTSTRAP_SERVERS", "localhost:29092")


class NullDlq:
    async def send_and_wait(self, *args: object, **kwargs: object) -> None:
        return None


async def test_consumer_started_before_its_topic_exists_still_consumes() -> None:
    """Every pod starts at once in Kubernetes, so a consumer can subscribe
    before the topic-creation Job has created its topic."""
    topic = f"devassist-test-late-{uuid.uuid4().hex[:8]}"
    received: list[Envelope] = []
    got = asyncio.Event()

    async def handler(env: Envelope) -> None:
        received.append(env)
        got.set()

    consumer = EventConsumer(
        BOOTSTRAP,
        f"{topic}-group",
        [EventType.REPO_INDEXED],
        handler,
        NullDlq(),
        start_from="latest",
    )
    consumer.topics = [topic]  # a throwaway topic instead of the real one
    stop = asyncio.Event()
    task = asyncio.create_task(consumer.run(stop))
    try:
        await asyncio.sleep(3)  # subscribed, topic still missing
        assert not consumer.ready.is_set(), "ready must wait for real partitions"

        admin = AIOKafkaAdminClient(bootstrap_servers=BOOTSTRAP)
        await admin.start()
        try:
            await admin.create_topics([NewTopic(topic, num_partitions=3, replication_factor=1)])
        finally:
            await admin.close()

        start = time.monotonic()
        await asyncio.wait_for(consumer.ready.wait(), timeout=60)
        producer = AIOKafkaProducer(bootstrap_servers=BOOTSTRAP)
        await producer.start()
        try:
            env = new_event(
                EventType.REPO_INDEXED,
                RepoIndexed(
                    repo_id=uuid.uuid4(), snapshot_id=uuid.uuid4(), commit_sha="a", status="ready"
                ),
                source="test",
            )
            await producer.send_and_wait(topic, env.to_bytes(), key=b"k")
        finally:
            await producer.stop()
        await asyncio.wait_for(got.wait(), timeout=30)
        assert received[0].event_id == env.event_id
        assert time.monotonic() - start < 60
    finally:
        stop.set()
        await asyncio.wait_for(task, timeout=30)
