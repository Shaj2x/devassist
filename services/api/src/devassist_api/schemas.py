"""Request and response bodies of the REST API.

Responses are built explicitly from ORM rows (`from_attributes`) so internal
columns such as encrypted tokens can never leak into a payload by accident.
"""

from __future__ import annotations

import uuid
from datetime import datetime
from decimal import Decimal
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field

# --- auth ---------------------------------------------------------------------


class TokenLogin(BaseModel):
    github_token: str = Field(min_length=1)


class UserOut(BaseModel):
    id: uuid.UUID
    login: str
    avatar_url: str | None = None


class Me(BaseModel):
    user: UserOut
    auth_mode: Literal["dev", "token"]
    github_connected: bool


class SessionOut(BaseModel):
    session_token: str
    user: UserOut


# --- repositories -------------------------------------------------------------


class ValidationSettings(BaseModel):
    """Optional per-repo overrides for the sandbox (otherwise auto-detected)."""

    model_config = ConfigDict(extra="forbid")

    language: Literal["python", "go", "node"] | None = None
    install_command: str | None = None
    test_command: str | None = None
    timeout_seconds: int | None = Field(default=None, ge=10, le=1800)


class RepoCreate(BaseModel):
    # "owner/name" on GitHub. With clone_url (local/dev repos) it is just a label.
    full_name: str = Field(pattern=r"^[\w.-]+/[\w.-]+$", max_length=255)
    clone_url: str | None = None
    default_branch: str | None = None
    config: ValidationSettings = Field(default_factory=ValidationSettings)


class SnapshotOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: uuid.UUID
    commit_sha: str
    status: str
    file_count: int
    chunk_count: int
    embedding_model: str | None
    error: str | None
    created_at: datetime
    finished_at: datetime | None


class RepoOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: uuid.UUID
    full_name: str
    clone_url: str
    default_branch: str
    config: dict[str, Any]
    is_github: bool
    created_at: datetime
    latest_snapshot: SnapshotOut | None = None


# --- jobs ---------------------------------------------------------------------


class JobCreate(BaseModel):
    repo_id: uuid.UUID
    task: str = Field(min_length=3, max_length=8000)
    max_iterations: int | None = Field(default=None, ge=1, le=10)
    base_commit_sha: str | None = Field(default=None, pattern=r"^[0-9a-f]{7,64}$")


class JobSummary(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: uuid.UUID
    repo_id: uuid.UUID
    repo_name: str
    task: str
    status: str
    current_iteration: int
    max_iterations: int
    total_cost_usd: Decimal
    created_at: datetime
    completed_at: datetime | None
    pr_url: str | None = None


class StepSummary(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: uuid.UUID
    sequence: int
    iteration: int
    agent: str
    status: str
    model: str | None
    input_tokens: int
    output_tokens: int
    cost_usd: Decimal
    latency_ms: int | None
    error: str | None
    started_at: datetime
    finished_at: datetime | None


class StepDetail(StepSummary):
    prompt: dict[str, Any]
    response: str | None
    parsed_output: dict[str, Any] | None


class ValidationOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: uuid.UUID
    status: str
    tests_status: str | None
    security_status: str | None
    static_status: str | None
    tests: dict[str, Any] | None
    security: dict[str, Any] | None
    static_analysis: dict[str, Any] | None
    error: str | None
    duration_ms: int | None
    created_at: datetime


class PatchOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    id: uuid.UUID
    iteration: int
    status: str
    diff: str
    files_changed: int
    additions: int
    deletions: int
    created_at: datetime
    validations: list[ValidationOut] = Field(default_factory=list)


class PullRequestOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    number: int
    url: str
    branch_name: str
    state: str


class JobDetail(JobSummary):
    base_commit_sha: str | None
    plan: dict[str, Any] | None
    review: dict[str, Any] | None
    review_feedback: str | None
    error: str | None
    total_input_tokens: int
    total_output_tokens: int
    steps: list[StepSummary]
    patches: list[PatchOut]
    pull_request: PullRequestOut | None
    # Latest live progress document from Redis, while the job runs.
    progress: dict[str, Any] | None = None


class RequestChanges(BaseModel):
    feedback: str = Field(min_length=3, max_length=8000)


class Reject(BaseModel):
    reason: str | None = Field(default=None, max_length=2000)


class Accepted(BaseModel):
    id: uuid.UUID
    status: str
