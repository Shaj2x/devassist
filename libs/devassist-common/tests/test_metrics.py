from __future__ import annotations

import uuid

import httpx
from devassist_common.events import EventType, RepoIndexed, new_event
from devassist_common.kafka import EventConsumer, PermanentError
from devassist_common.metrics import HTTP_REQUESTS, KAFKA_MESSAGES, instrument
from fastapi import FastAPI, HTTPException
from prometheus_client import REGISTRY


def sample(name: str, labels: dict[str, str]) -> float:
    return REGISTRY.get_sample_value(name, labels) or 0.0


async def test_requests_are_labelled_by_route_template() -> None:
    app = FastAPI()
    instrument(app)

    @app.get("/v1/things/{thing_id}")
    async def thing(thing_id: str) -> dict[str, str]:
        if thing_id == "missing":
            raise HTTPException(404)
        return {"id": thing_id}

    labels = {"route": "/v1/things/{thing_id}", "method": "GET", "status": "200"}
    before = sample("http_requests_total", labels)
    async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://t") as c:
        for i in range(3):
            assert (await c.get(f"/v1/things/{i}")).status_code == 200
        assert (await c.get("/v1/things/missing")).status_code == 404
        exposition = (await c.get("/metrics")).text

    assert sample("http_requests_total", labels) == before + 3
    assert sample("http_requests_total", {**labels, "status": "404"}) >= 1
    assert 'route="/v1/things/{thing_id}"' in exposition
    assert 'route="/metrics"' not in exposition
    assert HTTP_REQUESTS is not None


class NullDlq:
    async def send_and_wait(self, *args: object, **kwargs: object) -> None:
        return None


async def test_consumer_counts_processed_retried_and_dead_lettered() -> None:
    attempts = {"n": 0}

    async def flaky(env: object) -> None:
        attempts["n"] += 1
        if attempts["n"] == 1:
            raise RuntimeError("transient")

    async def broken(env: object) -> None:
        raise PermanentError("bad")

    env = new_event(
        EventType.REPO_INDEXED,
        RepoIndexed(
            repo_id=uuid.UUID(int=1),
            snapshot_id=uuid.UUID(int=2),
            commit_sha="abc",
            status="ready",
        ),
        source="test",
    )
    topic = "metrics-test-topic"

    def get(outcome: str) -> float:
        return sample("kafka_messages_total", {"topic": topic, "outcome": outcome})

    before = {o: get(o) for o in ("processed", "retried", "dead_lettered")}

    for handler in (flaky, broken):
        consumer = EventConsumer(
            "unused:9092", "g", [EventType.REPO_INDEXED], handler, NullDlq(), initial_backoff=0
        )
        await consumer.process(topic, env.to_bytes(), b"k", 0)

    assert get("processed") == before["processed"] + 1
    assert get("retried") == before["retried"] + 1
    assert get("dead_lettered") == before["dead_lettered"] + 1
    assert KAFKA_MESSAGES is not None
