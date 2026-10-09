"""Patch validation through the sandbox runner.

The orchestrator depends on the `Validator` protocol. `HttpValidator` calls
the runner's synchronous endpoint (used by the CLI and in Phase 4); the
Kafka-driven path (publish patch.generated, await validation.completed)
plugs in behind the same protocol.
"""

from __future__ import annotations

import time
import uuid
from dataclasses import dataclass
from typing import Any, Protocol

import httpx
from devassist_common.events import EventType, PatchGenerated, ValidationCompleted, ValidationConfig
from devassist_common.kafka import EventPublisher
from redis.asyncio import Redis


@dataclass(frozen=True)
class ValidationRequest:
    job_id: str
    patch_id: str
    iteration: int
    repo_id: str
    clone_url: str
    commit_sha: str
    diff: str
    config: dict[str, Any]


class Validator(Protocol):
    async def validate(self, req: ValidationRequest) -> ValidationCompleted: ...


class HttpValidator:
    def __init__(
        self, base_url: str, *, timeout: float = 600.0, client: httpx.AsyncClient | None = None
    ) -> None:
        self.client = client or httpx.AsyncClient(base_url=base_url, timeout=timeout)

    async def validate(self, req: ValidationRequest) -> ValidationCompleted:
        body: dict[str, Any] = {
            "clone_url": req.clone_url,
            "commit_sha": req.commit_sha,
            "diff": req.diff,
            "config": {
                k: req.config.get(k) for k in ("language", "install_command", "test_command")
            },
        }
        if req.config.get("timeout_seconds"):
            body["timeout_seconds"] = req.config["timeout_seconds"]
        resp = await self.client.post("/v1/validate", json=body)
        resp.raise_for_status()
        data = resp.json()
        # The sync endpoint does not know our ids; fill them in.
        data.update(job_id=req.job_id, patch_id=req.patch_id, iteration=req.iteration)
        return ValidationCompleted.model_validate(data)

    async def aclose(self) -> None:
        await self.client.aclose()


def result_key(patch_id: str) -> str:
    return f"devassist:validation:result:{patch_id}"


BLOCK_SLICE_SECONDS = 2.0


class KafkaValidator:
    """Validation over the event bus.

    Publishes patch.generated, then blocks on a per-patch Redis list. The
    orchestrator's validation.completed consumer pushes each result onto
    that list, so the waiting job need not be on the replica that happened
    to consume the event.
    """

    def __init__(self, publisher: EventPublisher, redis: Redis, *, timeout: float = 900.0) -> None:
        self.publisher, self.redis, self.timeout = publisher, redis, timeout

    async def validate(self, req: ValidationRequest) -> ValidationCompleted:
        cfg = ValidationConfig.model_validate(
            {
                k: req.config.get(k)
                for k in ("language", "install_command", "test_command", "timeout_seconds")
            }
        )
        await self.publisher.publish(
            EventType.PATCH_GENERATED,
            PatchGenerated(
                job_id=uuid.UUID(req.job_id),
                patch_id=uuid.UUID(req.patch_id),
                iteration=req.iteration,
                repo_id=uuid.UUID(req.repo_id),
                clone_url=req.clone_url,
                commit_sha=req.commit_sha,
                diff=req.diff,
                config=cfg,
            ),
            trace_id=req.job_id,
        )
        # Block in short slices: a single long BLPOP would outlive the Redis
        # client's socket read timeout (5 s by default) and fail.
        deadline = time.monotonic() + self.timeout
        while (remaining := deadline - time.monotonic()) > 0:
            popped = await self.redis.blpop(
                [result_key(req.patch_id)], timeout=min(BLOCK_SLICE_SECONDS, max(remaining, 0.1))
            )
            if popped is not None:
                return ValidationCompleted.model_validate_json(popped[1])
        raise TimeoutError(f"no validation result for patch {req.patch_id} within {self.timeout}s")
