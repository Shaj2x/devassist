"""Patch validation through the sandbox runner.

The orchestrator depends on the `Validator` protocol. `HttpValidator` calls
the runner's synchronous endpoint (used by the CLI and in Phase 4); the
Kafka-driven path (publish patch.generated, await validation.completed)
plugs in behind the same protocol.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Protocol

import httpx
from devassist_common.events import ValidationCompleted


@dataclass(frozen=True)
class ValidationRequest:
    job_id: str
    patch_id: str
    iteration: int
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
