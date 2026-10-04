"""Initial schema: users, repositories, snapshots, code chunks (pgvector),
jobs, agent steps, patches, validation runs, pull requests, processed events.

Status columns are VARCHAR + CHECK rather than native Postgres ENUMs so a new
status is a constraint change, not an ALTER TYPE.

Revision ID: 0001
Revises:
Create Date: 2026-10-04
"""

from collections.abc import Sequence

import sqlalchemy as sa
from alembic import op
from pgvector.sqlalchemy import Vector
from sqlalchemy.dialects import postgresql

revision: str = "0001"
down_revision: str | None = None
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # pgvector provides the `vector` column type and the HNSW index method.
    op.execute("CREATE EXTENSION IF NOT EXISTS vector")

    op.create_table(
        "processed_events",
        sa.Column("consumer", sa.String(length=64), nullable=False),
        sa.Column("event_id", sa.Uuid(), nullable=False),
        sa.Column("event_type", sa.String(length=64), nullable=False),
        sa.Column(
            "processed_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.PrimaryKeyConstraint("consumer", "event_id", name=op.f("pk_processed_events")),
    )
    op.create_table(
        "users",
        sa.Column("github_login", sa.String(length=100), nullable=False),
        sa.Column("github_id", sa.BigInteger(), nullable=True),
        sa.Column("email", sa.String(length=320), nullable=True),
        sa.Column("avatar_url", sa.Text(), nullable=True),
        sa.Column("github_token_encrypted", sa.LargeBinary(), nullable=True),
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.PrimaryKeyConstraint("id", name=op.f("pk_users")),
        sa.UniqueConstraint("github_id", name=op.f("uq_users_github_id")),
        sa.UniqueConstraint("github_login", name=op.f("uq_users_github_login")),
    )
    op.create_table(
        "repositories",
        sa.Column("owner_id", sa.Uuid(), nullable=False),
        sa.Column("full_name", sa.String(length=255), nullable=False),
        sa.Column("clone_url", sa.Text(), nullable=False),
        sa.Column("default_branch", sa.String(length=255), nullable=False),
        sa.Column(
            "config", postgresql.JSONB(astext_type=sa.Text()), server_default="{}", nullable=False
        ),
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.ForeignKeyConstraint(
            ["owner_id"],
            ["users.id"],
            name=op.f("fk_repositories_owner_id_users"),
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id", name=op.f("pk_repositories")),
        sa.UniqueConstraint(
            "owner_id", "full_name", name=op.f("uq_repositories_owner_id_full_name")
        ),
    )
    op.create_table(
        "repo_snapshots",
        sa.Column("repo_id", sa.Uuid(), nullable=False),
        sa.Column("commit_sha", sa.String(length=64), nullable=False),
        sa.Column("branch", sa.String(length=255), nullable=True),
        sa.Column(
            "status",
            sa.String(length=32),
            nullable=False,
        ),
        sa.Column("file_count", sa.Integer(), server_default="0", nullable=False),
        sa.Column("chunk_count", sa.Integer(), server_default="0", nullable=False),
        sa.Column("error", sa.Text(), nullable=True),
        sa.Column("started_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("finished_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.CheckConstraint(
            "status IN ('pending', 'indexing', 'ready', 'failed')",
            name=op.f("ck_repo_snapshots_snapshot_status"),
        ),
        sa.ForeignKeyConstraint(
            ["repo_id"],
            ["repositories.id"],
            name=op.f("fk_repo_snapshots_repo_id_repositories"),
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id", name=op.f("pk_repo_snapshots")),
        sa.UniqueConstraint(
            "repo_id", "commit_sha", name=op.f("uq_repo_snapshots_repo_id_commit_sha")
        ),
    )
    op.create_table(
        "code_chunks",
        sa.Column("snapshot_id", sa.Uuid(), nullable=False),
        sa.Column("repo_id", sa.Uuid(), nullable=False),
        sa.Column("file_path", sa.Text(), nullable=False),
        sa.Column("language", sa.String(length=32), nullable=True),
        sa.Column("symbol_name", sa.String(length=255), nullable=True),
        sa.Column("symbol_kind", sa.String(length=32), nullable=True),
        sa.Column("start_line", sa.Integer(), nullable=False),
        sa.Column("end_line", sa.Integer(), nullable=False),
        sa.Column("content", sa.Text(), nullable=False),
        sa.Column("content_hash", sa.String(length=64), nullable=False),
        sa.Column("token_count", sa.Integer(), server_default="0", nullable=False),
        sa.Column("embedding", Vector(1024), nullable=True),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.CheckConstraint("end_line >= start_line", name=op.f("ck_code_chunks_line_range")),
        sa.ForeignKeyConstraint(
            ["repo_id"],
            ["repositories.id"],
            name=op.f("fk_code_chunks_repo_id_repositories"),
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["snapshot_id"],
            ["repo_snapshots.id"],
            name=op.f("fk_code_chunks_snapshot_id_repo_snapshots"),
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id", name=op.f("pk_code_chunks")),
    )
    op.create_index(
        "ix_code_chunks_embedding_hnsw",
        "code_chunks",
        ["embedding"],
        unique=False,
        postgresql_using="hnsw",
        postgresql_with={"m": 16, "ef_construction": 64},
        postgresql_ops={"embedding": "vector_cosine_ops"},
    )
    op.create_index(
        "ix_code_chunks_repo_content_hash", "code_chunks", ["repo_id", "content_hash"], unique=False
    )
    op.create_index(
        "ix_code_chunks_repo_symbol", "code_chunks", ["repo_id", "symbol_name"], unique=False
    )
    op.create_index(
        "ix_code_chunks_snapshot_path", "code_chunks", ["snapshot_id", "file_path"], unique=False
    )
    op.create_table(
        "jobs",
        sa.Column("repo_id", sa.Uuid(), nullable=False),
        sa.Column("snapshot_id", sa.Uuid(), nullable=True),
        sa.Column("created_by", sa.Uuid(), nullable=True),
        sa.Column("task", sa.Text(), nullable=False),
        sa.Column(
            "status",
            sa.String(length=32),
            nullable=False,
        ),
        sa.Column("base_commit_sha", sa.String(length=64), nullable=True),
        sa.Column("max_iterations", sa.Integer(), server_default="3", nullable=False),
        sa.Column("current_iteration", sa.Integer(), server_default="0", nullable=False),
        sa.Column("plan", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("reviewer_summary", sa.Text(), nullable=True),
        sa.Column("review_feedback", sa.Text(), nullable=True),
        sa.Column("error", sa.Text(), nullable=True),
        sa.Column("total_input_tokens", sa.Integer(), server_default="0", nullable=False),
        sa.Column("total_output_tokens", sa.Integer(), server_default="0", nullable=False),
        sa.Column(
            "total_cost_usd", sa.Numeric(precision=12, scale=6), server_default="0", nullable=False
        ),
        sa.Column("completed_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.CheckConstraint(
            "status IN ('queued', 'planning', 'coding', 'testing', 'validating', 'debugging', 'reviewing', 'awaiting_review', 'changes_requested', 'approved', 'rejected', 'pr_opened', 'failed', 'cancelled')",
            name=op.f("ck_jobs_job_status"),
        ),
        sa.CheckConstraint(
            "max_iterations BETWEEN 1 AND 10", name=op.f("ck_jobs_max_iterations_range")
        ),
        sa.ForeignKeyConstraint(
            ["created_by"], ["users.id"], name=op.f("fk_jobs_created_by_users"), ondelete="SET NULL"
        ),
        sa.ForeignKeyConstraint(
            ["repo_id"],
            ["repositories.id"],
            name=op.f("fk_jobs_repo_id_repositories"),
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["snapshot_id"],
            ["repo_snapshots.id"],
            name=op.f("fk_jobs_snapshot_id_repo_snapshots"),
            ondelete="SET NULL",
        ),
        sa.PrimaryKeyConstraint("id", name=op.f("pk_jobs")),
    )
    op.create_index("ix_jobs_repo_created", "jobs", ["repo_id", "created_at"], unique=False)
    op.create_index("ix_jobs_status", "jobs", ["status"], unique=False)
    op.create_table(
        "agent_steps",
        sa.Column("job_id", sa.Uuid(), nullable=False),
        sa.Column("iteration", sa.Integer(), nullable=False),
        sa.Column("sequence", sa.Integer(), nullable=False),
        sa.Column(
            "agent",
            sa.String(length=32),
            nullable=False,
        ),
        sa.Column(
            "status",
            sa.String(length=32),
            nullable=False,
        ),
        sa.Column("provider", sa.String(length=32), nullable=True),
        sa.Column("model", sa.String(length=100), nullable=True),
        sa.Column("prompt", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.Column("response", sa.Text(), nullable=True),
        sa.Column("parsed_output", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("input_tokens", sa.Integer(), server_default="0", nullable=False),
        sa.Column("output_tokens", sa.Integer(), server_default="0", nullable=False),
        sa.Column(
            "cost_usd", sa.Numeric(precision=12, scale=6), server_default="0", nullable=False
        ),
        sa.Column("latency_ms", sa.Integer(), nullable=True),
        sa.Column("error", sa.Text(), nullable=True),
        sa.Column(
            "started_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.Column("finished_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.CheckConstraint(
            "agent IN ('planner', 'coder', 'tester', 'debugger', 'reviewer')",
            name=op.f("ck_agent_steps_agent_name"),
        ),
        sa.CheckConstraint(
            "status IN ('running', 'succeeded', 'failed')",
            name=op.f("ck_agent_steps_agent_step_status"),
        ),
        sa.ForeignKeyConstraint(
            ["job_id"], ["jobs.id"], name=op.f("fk_agent_steps_job_id_jobs"), ondelete="CASCADE"
        ),
        sa.PrimaryKeyConstraint("id", name=op.f("pk_agent_steps")),
        sa.UniqueConstraint("job_id", "sequence", name=op.f("uq_agent_steps_job_id_sequence")),
    )
    op.create_table(
        "patches",
        sa.Column("job_id", sa.Uuid(), nullable=False),
        sa.Column("iteration", sa.Integer(), nullable=False),
        sa.Column("diff", sa.Text(), nullable=False),
        sa.Column(
            "status",
            sa.String(length=32),
            nullable=False,
        ),
        sa.Column("files_changed", sa.Integer(), server_default="0", nullable=False),
        sa.Column("additions", sa.Integer(), server_default="0", nullable=False),
        sa.Column("deletions", sa.Integer(), server_default="0", nullable=False),
        sa.Column("created_by_step_id", sa.Uuid(), nullable=True),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.CheckConstraint(
            "status IN ('generated', 'validating', 'passed', 'failed', 'superseded', 'approved', 'rejected')",
            name=op.f("ck_patches_patch_status"),
        ),
        sa.ForeignKeyConstraint(
            ["created_by_step_id"],
            ["agent_steps.id"],
            name=op.f("fk_patches_created_by_step_id_agent_steps"),
            ondelete="SET NULL",
        ),
        sa.ForeignKeyConstraint(
            ["job_id"], ["jobs.id"], name=op.f("fk_patches_job_id_jobs"), ondelete="CASCADE"
        ),
        sa.PrimaryKeyConstraint("id", name=op.f("pk_patches")),
        sa.UniqueConstraint("job_id", "iteration", name=op.f("uq_patches_job_id_iteration")),
    )
    op.create_table(
        "pull_requests",
        sa.Column("job_id", sa.Uuid(), nullable=False),
        sa.Column("patch_id", sa.Uuid(), nullable=False),
        sa.Column("repo_id", sa.Uuid(), nullable=False),
        sa.Column("number", sa.Integer(), nullable=False),
        sa.Column("url", sa.Text(), nullable=False),
        sa.Column("branch_name", sa.String(length=255), nullable=False),
        sa.Column(
            "state",
            sa.String(length=32),
            nullable=False,
        ),
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.CheckConstraint(
            "state IN ('open', 'closed', 'merged')",
            name=op.f("ck_pull_requests_pull_request_state"),
        ),
        sa.ForeignKeyConstraint(
            ["job_id"], ["jobs.id"], name=op.f("fk_pull_requests_job_id_jobs"), ondelete="CASCADE"
        ),
        sa.ForeignKeyConstraint(
            ["patch_id"],
            ["patches.id"],
            name=op.f("fk_pull_requests_patch_id_patches"),
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["repo_id"],
            ["repositories.id"],
            name=op.f("fk_pull_requests_repo_id_repositories"),
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id", name=op.f("pk_pull_requests")),
        sa.UniqueConstraint("job_id", name=op.f("uq_pull_requests_job_id")),
    )
    op.create_table(
        "validation_runs",
        sa.Column("patch_id", sa.Uuid(), nullable=False),
        sa.Column("source_event_id", sa.Uuid(), nullable=True),
        sa.Column(
            "status",
            sa.String(length=32),
            nullable=False,
        ),
        sa.Column(
            "tests_status",
            sa.String(length=32),
            nullable=True,
        ),
        sa.Column(
            "security_status",
            sa.String(length=32),
            nullable=True,
        ),
        sa.Column(
            "static_status",
            sa.String(length=32),
            nullable=True,
        ),
        sa.Column("tests", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("security", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("static_analysis", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("error", sa.Text(), nullable=True),
        sa.Column("duration_ms", sa.Integer(), nullable=True),
        sa.Column("started_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("finished_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("id", sa.Uuid(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.CheckConstraint(
            "security_status IN ('passed', 'failed', 'skipped', 'error')",
            name=op.f("ck_validation_runs_check_status_security"),
        ),
        sa.CheckConstraint(
            "static_status IN ('passed', 'failed', 'skipped', 'error')",
            name=op.f("ck_validation_runs_check_status_static"),
        ),
        sa.CheckConstraint(
            "status IN ('queued', 'running', 'passed', 'failed', 'error', 'timeout')",
            name=op.f("ck_validation_runs_validation_status"),
        ),
        sa.CheckConstraint(
            "tests_status IN ('passed', 'failed', 'skipped', 'error')",
            name=op.f("ck_validation_runs_check_status_tests"),
        ),
        sa.ForeignKeyConstraint(
            ["patch_id"],
            ["patches.id"],
            name=op.f("fk_validation_runs_patch_id_patches"),
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id", name=op.f("pk_validation_runs")),
        sa.UniqueConstraint("source_event_id", name=op.f("uq_validation_runs_source_event_id")),
    )


def downgrade() -> None:
    op.drop_table("validation_runs")
    op.drop_table("pull_requests")
    op.drop_table("patches")
    op.drop_table("agent_steps")
    op.drop_index("ix_jobs_status", table_name="jobs")
    op.drop_index("ix_jobs_repo_created", table_name="jobs")
    op.drop_table("jobs")
    op.drop_index("ix_code_chunks_snapshot_path", table_name="code_chunks")
    op.drop_index("ix_code_chunks_repo_symbol", table_name="code_chunks")
    op.drop_index("ix_code_chunks_repo_content_hash", table_name="code_chunks")
    op.drop_index(
        "ix_code_chunks_embedding_hnsw",
        table_name="code_chunks",
        postgresql_using="hnsw",
        postgresql_with={"m": 16, "ef_construction": 64},
        postgresql_ops={"embedding": "vector_cosine_ops"},
    )
    op.drop_table("code_chunks")
    op.drop_table("repo_snapshots")
    op.drop_table("repositories")
    op.drop_table("users")
    op.drop_table("processed_events")
