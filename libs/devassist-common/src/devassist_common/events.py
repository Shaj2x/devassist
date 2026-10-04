"""The Kafka event envelope and typed payloads.

This module mirrors proto/events/*.schema.json. The contract tests parse the
fixtures in proto/events/examples with these models (and the Go services do
the same with their structs), so a drift between languages fails CI.
"""

from __future__ import annotations

import uuid
from datetime import UTC, datetime
from enum import StrEnum
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field


class EventType(StrEnum):
    REPO_REGISTERED = "repo.registered"
    REPO_INDEXED = "repo.indexed"
    JOB_CREATED = "job.created"
    PATCH_GENERATED = "patch.generated"
    VALIDATION_COMPLETED = "validation.completed"
    JOB_COMPLETED = "job.completed"


# Topic name == event type. Each topic also has a dead-letter topic.
TOPICS: tuple[str, ...] = tuple(t.value for t in EventType)


def dlq_topic(topic: str) -> str:
    return f"{topic}.dlq"


class _Payload(BaseModel):
    model_config = ConfigDict(extra="allow")


class RepoRegistered(_Payload):
    repo_id: uuid.UUID
    full_name: str
    clone_url: str
    default_branch: str
    commit_sha: str | None = None


class RepoIndexed(_Payload):
    repo_id: uuid.UUID
    snapshot_id: uuid.UUID
    commit_sha: str
    status: Literal["ready", "failed"]
    file_count: int = 0
    chunk_count: int = 0
    error: str | None = None


class JobCreated(_Payload):
    job_id: uuid.UUID
    repo_id: uuid.UUID
    task: str = Field(min_length=1)
    base_commit_sha: str | None = None
    max_iterations: int = Field(ge=1, le=10)


class ValidationConfig(_Payload):
    language: str | None = None
    install_command: str | None = None
    test_command: str | None = None
    timeout_seconds: int | None = Field(default=None, ge=1)


class PatchGenerated(_Payload):
    job_id: uuid.UUID
    patch_id: uuid.UUID
    iteration: int = Field(ge=1)
    repo_id: uuid.UUID
    clone_url: str
    commit_sha: str
    diff: str
    config: ValidationConfig = Field(default_factory=ValidationConfig)


CheckStatus = Literal["passed", "failed", "skipped", "error"]


class Finding(_Payload):
    message: str
    tool: str | None = None
    rule_id: str | None = None
    severity: str | None = None
    file: str | None = None
    line: int | None = None


class CheckResult(_Payload):
    status: CheckStatus
    tool: str | None = None
    summary: str | None = None
    passed: int = 0
    failed: int = 0
    skipped: int = 0
    findings: list[Finding] = Field(default_factory=list)
    log: str | None = None
    duration_ms: int = 0


class ValidationCompleted(_Payload):
    job_id: uuid.UUID
    patch_id: uuid.UUID
    iteration: int = Field(ge=1)
    status: Literal["passed", "failed", "error", "timeout"]
    tests: CheckResult
    security: CheckResult
    static_analysis: CheckResult
    duration_ms: int = 0
    error: str | None = None


class JobCompleted(_Payload):
    job_id: uuid.UUID
    status: Literal["awaiting_review", "failed", "cancelled"]
    final_patch_id: uuid.UUID | None = None
    iterations: int = Field(ge=0)
    error: str | None = None


PAYLOAD_MODELS: dict[EventType, type[_Payload]] = {
    EventType.REPO_REGISTERED: RepoRegistered,
    EventType.REPO_INDEXED: RepoIndexed,
    EventType.JOB_CREATED: JobCreated,
    EventType.PATCH_GENERATED: PatchGenerated,
    EventType.VALIDATION_COMPLETED: ValidationCompleted,
    EventType.JOB_COMPLETED: JobCompleted,
}


class Envelope(BaseModel):
    model_config = ConfigDict(extra="forbid")

    event_id: uuid.UUID = Field(default_factory=uuid.uuid4)
    type: EventType
    version: int = Field(default=1, ge=1)
    timestamp: datetime = Field(default_factory=lambda: datetime.now(UTC))
    source: str
    trace_id: str | None = None
    payload: dict[str, Any]

    def typed_payload(self) -> _Payload:
        """Validate and return the payload as its type-specific model."""
        return PAYLOAD_MODELS[self.type].model_validate(self.payload)

    def to_bytes(self) -> bytes:
        return self.model_dump_json().encode()

    @classmethod
    def from_bytes(cls, raw: bytes) -> Envelope:
        return cls.model_validate_json(raw)


def new_event(
    event_type: EventType, payload: _Payload, *, source: str, trace_id: str | None = None
) -> Envelope:
    """Build an envelope around a typed payload."""
    return Envelope(
        type=event_type,
        source=source,
        trace_id=trace_id,
        payload=payload.model_dump(mode="json"),
    )
