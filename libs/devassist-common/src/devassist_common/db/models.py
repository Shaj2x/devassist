"""The DevAssist relational model.

    users ─┬─< repositories ─┬─< repo_snapshots ──< code_chunks (pgvector)
           │                 └─< jobs ─┬─< agent_steps
           └──────────────────────<    ├─< patches ──< validation_runs
                                       └── pull_requests

Indexer (Go) writes repo_snapshots and code_chunks directly. The orchestrator
writes jobs, agent_steps, patches and validation_runs. The api writes users,
repositories, jobs (creation, review decisions) and pull_requests.
"""

from __future__ import annotations

import uuid
from datetime import datetime
from decimal import Decimal
from enum import StrEnum
from typing import Any

from pgvector.sqlalchemy import Vector
from sqlalchemy import (
    BigInteger,
    CheckConstraint,
    DateTime,
    ForeignKey,
    Index,
    Integer,
    LargeBinary,
    Numeric,
    String,
    Text,
    UniqueConstraint,
    func,
)
from sqlalchemy.dialects.postgresql import JSONB
from sqlalchemy.orm import Mapped, mapped_column, relationship

from devassist_common.db.base import Base, Timestamps, UUIDPrimaryKey, str_enum

# Matches EMBEDDING_DIMENSIONS in .env.example. OpenAI text-embedding-3-small
# (with dimensions=1024), voyage-code-3 and the local hashing embedder all
# produce 1024-d vectors. Changing it requires a migration.
EMBEDDING_DIMENSIONS = 1024


# ---------------------------------------------------------------------------
# Status enums
# ---------------------------------------------------------------------------


class SnapshotStatus(StrEnum):
    PENDING = "pending"
    INDEXING = "indexing"
    READY = "ready"
    FAILED = "failed"


class JobStatus(StrEnum):
    QUEUED = "queued"
    PLANNING = "planning"
    CODING = "coding"
    TESTING = "testing"
    VALIDATING = "validating"
    DEBUGGING = "debugging"
    REVIEWING = "reviewing"
    AWAITING_REVIEW = "awaiting_review"
    CHANGES_REQUESTED = "changes_requested"
    APPROVED = "approved"
    REJECTED = "rejected"
    PR_OPENED = "pr_opened"
    FAILED = "failed"
    CANCELLED = "cancelled"


class AgentName(StrEnum):
    PLANNER = "planner"
    CODER = "coder"
    TESTER = "tester"
    DEBUGGER = "debugger"
    REVIEWER = "reviewer"


class AgentStepStatus(StrEnum):
    RUNNING = "running"
    SUCCEEDED = "succeeded"
    FAILED = "failed"


class PatchStatus(StrEnum):
    GENERATED = "generated"
    VALIDATING = "validating"
    PASSED = "passed"
    FAILED = "failed"
    SUPERSEDED = "superseded"
    APPROVED = "approved"
    REJECTED = "rejected"


class ValidationStatus(StrEnum):
    QUEUED = "queued"
    RUNNING = "running"
    PASSED = "passed"
    FAILED = "failed"
    ERROR = "error"
    TIMEOUT = "timeout"


class CheckStatus(StrEnum):
    PASSED = "passed"
    FAILED = "failed"
    SKIPPED = "skipped"
    ERROR = "error"


class PullRequestState(StrEnum):
    OPEN = "open"
    CLOSED = "closed"
    MERGED = "merged"


# ---------------------------------------------------------------------------
# Tables
# ---------------------------------------------------------------------------


class User(UUIDPrimaryKey, Timestamps, Base):
    __tablename__ = "users"

    github_login: Mapped[str] = mapped_column(String(100), unique=True)
    github_id: Mapped[int | None] = mapped_column(BigInteger, unique=True)
    email: Mapped[str | None] = mapped_column(String(320))
    avatar_url: Mapped[str | None] = mapped_column(Text)
    # GitHub token (PAT or OAuth), encrypted with TOKEN_ENCRYPTION_KEY. Never logged.
    github_token_encrypted: Mapped[bytes | None] = mapped_column(LargeBinary)

    repositories: Mapped[list[Repository]] = relationship(back_populates="owner")


class Repository(UUIDPrimaryKey, Timestamps, Base):
    __tablename__ = "repositories"
    __table_args__ = (UniqueConstraint("owner_id", "full_name"),)

    owner_id: Mapped[uuid.UUID] = mapped_column(ForeignKey("users.id", ondelete="CASCADE"))
    full_name: Mapped[str] = mapped_column(String(255))  # "owner/name"
    clone_url: Mapped[str] = mapped_column(Text)
    default_branch: Mapped[str] = mapped_column(String(255), default="main")
    # Validation overrides: language, install_command, test_command, timeout_seconds.
    config: Mapped[dict[str, Any]] = mapped_column(JSONB, default=dict, server_default="{}")

    owner: Mapped[User] = relationship(back_populates="repositories")
    snapshots: Mapped[list[RepoSnapshot]] = relationship(
        back_populates="repository", cascade="all, delete-orphan"
    )
    jobs: Mapped[list[Job]] = relationship(back_populates="repository")


class RepoSnapshot(UUIDPrimaryKey, Timestamps, Base):
    """One indexed commit of a repository. Unique per (repo, sha) so
    reprocessing a duplicate `repo.registered` event is a no-op."""

    __tablename__ = "repo_snapshots"
    __table_args__ = (UniqueConstraint("repo_id", "commit_sha"),)

    repo_id: Mapped[uuid.UUID] = mapped_column(ForeignKey("repositories.id", ondelete="CASCADE"))
    commit_sha: Mapped[str] = mapped_column(String(64))
    branch: Mapped[str | None] = mapped_column(String(255))
    status: Mapped[SnapshotStatus] = mapped_column(
        str_enum(SnapshotStatus, "snapshot_status"), default=SnapshotStatus.PENDING
    )
    file_count: Mapped[int] = mapped_column(Integer, default=0, server_default="0")
    chunk_count: Mapped[int] = mapped_column(Integer, default=0, server_default="0")
    error: Mapped[str | None] = mapped_column(Text)
    started_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    finished_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))

    repository: Mapped[Repository] = relationship(back_populates="snapshots")


class CodeChunk(UUIDPrimaryKey, Base):
    """A function, class or line window of a source file, with its embedding.

    `content_hash` lets incremental reindexing reuse an embedding when a chunk
    is unchanged between commits instead of paying for it again."""

    __tablename__ = "code_chunks"
    __table_args__ = (
        Index("ix_code_chunks_snapshot_path", "snapshot_id", "file_path"),
        Index("ix_code_chunks_repo_symbol", "repo_id", "symbol_name"),
        Index("ix_code_chunks_repo_content_hash", "repo_id", "content_hash"),
        # HNSW over cosine distance: better recall/latency than ivfflat and
        # needs no training step, so it works on an empty table.
        Index(
            "ix_code_chunks_embedding_hnsw",
            "embedding",
            postgresql_using="hnsw",
            postgresql_with={"m": 16, "ef_construction": 64},
            postgresql_ops={"embedding": "vector_cosine_ops"},
        ),
        CheckConstraint("end_line >= start_line", name="line_range"),
    )

    snapshot_id: Mapped[uuid.UUID] = mapped_column(
        ForeignKey("repo_snapshots.id", ondelete="CASCADE")
    )
    # Denormalised from the snapshot so search can filter by repo without a join.
    repo_id: Mapped[uuid.UUID] = mapped_column(ForeignKey("repositories.id", ondelete="CASCADE"))
    file_path: Mapped[str] = mapped_column(Text)
    language: Mapped[str | None] = mapped_column(String(32))
    symbol_name: Mapped[str | None] = mapped_column(String(255))
    symbol_kind: Mapped[str | None] = mapped_column(String(32))  # function, class, method, window
    start_line: Mapped[int] = mapped_column(Integer)
    end_line: Mapped[int] = mapped_column(Integer)
    content: Mapped[str] = mapped_column(Text)
    content_hash: Mapped[str] = mapped_column(String(64))
    token_count: Mapped[int] = mapped_column(Integer, default=0, server_default="0")
    embedding: Mapped[list[float] | None] = mapped_column(Vector(EMBEDDING_DIMENSIONS))
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())


class Job(UUIDPrimaryKey, Timestamps, Base):
    __tablename__ = "jobs"
    __table_args__ = (
        Index("ix_jobs_repo_created", "repo_id", "created_at"),
        Index("ix_jobs_status", "status"),
        CheckConstraint("max_iterations BETWEEN 1 AND 10", name="max_iterations_range"),
    )

    repo_id: Mapped[uuid.UUID] = mapped_column(ForeignKey("repositories.id", ondelete="CASCADE"))
    snapshot_id: Mapped[uuid.UUID | None] = mapped_column(
        ForeignKey("repo_snapshots.id", ondelete="SET NULL")
    )
    created_by: Mapped[uuid.UUID | None] = mapped_column(
        ForeignKey("users.id", ondelete="SET NULL")
    )
    task: Mapped[str] = mapped_column(Text)
    status: Mapped[JobStatus] = mapped_column(
        str_enum(JobStatus, "job_status"), default=JobStatus.QUEUED
    )
    base_commit_sha: Mapped[str | None] = mapped_column(String(64))
    max_iterations: Mapped[int] = mapped_column(Integer, default=3, server_default="3")
    current_iteration: Mapped[int] = mapped_column(Integer, default=0, server_default="0")
    plan: Mapped[dict[str, Any] | None] = mapped_column(JSONB)
    reviewer_summary: Mapped[str | None] = mapped_column(Text)
    review_feedback: Mapped[str | None] = mapped_column(Text)  # human "request changes" note
    error: Mapped[str | None] = mapped_column(Text)
    total_input_tokens: Mapped[int] = mapped_column(Integer, default=0, server_default="0")
    total_output_tokens: Mapped[int] = mapped_column(Integer, default=0, server_default="0")
    total_cost_usd: Mapped[Decimal] = mapped_column(
        Numeric(12, 6), default=Decimal(0), server_default="0"
    )
    completed_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))

    repository: Mapped[Repository] = relationship(back_populates="jobs")
    steps: Mapped[list[AgentStep]] = relationship(
        back_populates="job", order_by="AgentStep.sequence", cascade="all, delete-orphan"
    )
    patches: Mapped[list[Patch]] = relationship(
        back_populates="job", order_by="Patch.iteration", cascade="all, delete-orphan"
    )
    pull_request: Mapped[PullRequest | None] = relationship(back_populates="job")


class AgentStep(UUIDPrimaryKey, Base):
    """One LLM call by one agent: the full prompt, response, cost and latency.
    This is what the dashboard's timeline renders."""

    __tablename__ = "agent_steps"
    __table_args__ = (UniqueConstraint("job_id", "sequence"),)

    job_id: Mapped[uuid.UUID] = mapped_column(ForeignKey("jobs.id", ondelete="CASCADE"))
    iteration: Mapped[int] = mapped_column(Integer)
    sequence: Mapped[int] = mapped_column(Integer)  # monotonically increasing per job
    agent: Mapped[AgentName] = mapped_column(str_enum(AgentName, "agent_name"))
    status: Mapped[AgentStepStatus] = mapped_column(
        str_enum(AgentStepStatus, "agent_step_status"), default=AgentStepStatus.RUNNING
    )
    provider: Mapped[str | None] = mapped_column(String(32))
    model: Mapped[str | None] = mapped_column(String(100))
    # Input as sent to the model: {"system": str, "messages": [...]}
    prompt: Mapped[dict[str, Any]] = mapped_column(JSONB)
    response: Mapped[str | None] = mapped_column(Text)
    parsed_output: Mapped[dict[str, Any] | None] = mapped_column(JSONB)
    input_tokens: Mapped[int] = mapped_column(Integer, default=0, server_default="0")
    output_tokens: Mapped[int] = mapped_column(Integer, default=0, server_default="0")
    cost_usd: Mapped[Decimal] = mapped_column(
        Numeric(12, 6), default=Decimal(0), server_default="0"
    )
    latency_ms: Mapped[int | None] = mapped_column(Integer)
    error: Mapped[str | None] = mapped_column(Text)
    started_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())
    finished_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))

    job: Mapped[Job] = relationship(back_populates="steps")


class Patch(UUIDPrimaryKey, Base):
    """A candidate diff. One per loop iteration; unique (job, iteration)
    makes re-publishing a patch idempotent."""

    __tablename__ = "patches"
    __table_args__ = (UniqueConstraint("job_id", "iteration"),)

    job_id: Mapped[uuid.UUID] = mapped_column(ForeignKey("jobs.id", ondelete="CASCADE"))
    iteration: Mapped[int] = mapped_column(Integer)
    diff: Mapped[str] = mapped_column(Text)
    status: Mapped[PatchStatus] = mapped_column(
        str_enum(PatchStatus, "patch_status"), default=PatchStatus.GENERATED
    )
    files_changed: Mapped[int] = mapped_column(Integer, default=0, server_default="0")
    additions: Mapped[int] = mapped_column(Integer, default=0, server_default="0")
    deletions: Mapped[int] = mapped_column(Integer, default=0, server_default="0")
    created_by_step_id: Mapped[uuid.UUID | None] = mapped_column(
        ForeignKey("agent_steps.id", ondelete="SET NULL")
    )
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), server_default=func.now())

    job: Mapped[Job] = relationship(back_populates="patches")
    validation_runs: Mapped[list[ValidationRun]] = relationship(
        back_populates="patch", cascade="all, delete-orphan"
    )


class ValidationRun(UUIDPrimaryKey, Timestamps, Base):
    """Sandbox results for one patch, split into tests / security / static
    analysis. Each section is the CheckResult JSON from the event contract."""

    __tablename__ = "validation_runs"

    patch_id: Mapped[uuid.UUID] = mapped_column(ForeignKey("patches.id", ondelete="CASCADE"))
    # event_id of the validation.completed event; unique for idempotent inserts.
    source_event_id: Mapped[uuid.UUID | None] = mapped_column(unique=True)
    status: Mapped[ValidationStatus] = mapped_column(
        str_enum(ValidationStatus, "validation_status"), default=ValidationStatus.QUEUED
    )
    tests_status: Mapped[CheckStatus | None] = mapped_column(
        str_enum(CheckStatus, "check_status_tests")
    )
    security_status: Mapped[CheckStatus | None] = mapped_column(
        str_enum(CheckStatus, "check_status_security")
    )
    static_status: Mapped[CheckStatus | None] = mapped_column(
        str_enum(CheckStatus, "check_status_static")
    )
    tests: Mapped[dict[str, Any] | None] = mapped_column(JSONB)
    security: Mapped[dict[str, Any] | None] = mapped_column(JSONB)
    static_analysis: Mapped[dict[str, Any] | None] = mapped_column(JSONB)
    error: Mapped[str | None] = mapped_column(Text)
    duration_ms: Mapped[int | None] = mapped_column(Integer)
    started_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    finished_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))

    patch: Mapped[Patch] = relationship(back_populates="validation_runs")


class PullRequest(UUIDPrimaryKey, Timestamps, Base):
    __tablename__ = "pull_requests"

    job_id: Mapped[uuid.UUID] = mapped_column(
        ForeignKey("jobs.id", ondelete="CASCADE"), unique=True
    )
    patch_id: Mapped[uuid.UUID] = mapped_column(ForeignKey("patches.id", ondelete="CASCADE"))
    repo_id: Mapped[uuid.UUID] = mapped_column(ForeignKey("repositories.id", ondelete="CASCADE"))
    number: Mapped[int] = mapped_column(Integer)
    url: Mapped[str] = mapped_column(Text)
    branch_name: Mapped[str] = mapped_column(String(255))
    state: Mapped[PullRequestState] = mapped_column(
        str_enum(PullRequestState, "pull_request_state"), default=PullRequestState.OPEN
    )

    job: Mapped[Job] = relationship(back_populates="pull_request")


class ProcessedEvent(Base):
    """Idempotency ledger. A consumer inserts (consumer, event_id) in the same
    transaction as its side effects; a redelivered event hits the primary key
    and is skipped."""

    __tablename__ = "processed_events"

    consumer: Mapped[str] = mapped_column(String(64), primary_key=True)
    event_id: Mapped[uuid.UUID] = mapped_column(primary_key=True)
    event_type: Mapped[str] = mapped_column(String(64))
    processed_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now()
    )
