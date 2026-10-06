"""Snapshot embedding model and server-side UUID defaults.

- repo_snapshots.embedding_model: embeddings are only reused across snapshots
  produced by the same model.
- Every UUID primary key gets DEFAULT gen_random_uuid(), so services that
  insert with raw SQL (the Go indexer) need not generate ids themselves.

Revision ID: 0002
Revises: 0001
Create Date: 2026-10-06
"""

from collections.abc import Sequence

import sqlalchemy as sa
from alembic import op

revision: str = "0002"
down_revision: str | None = "0001"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None

UUID_TABLES = (
    "users",
    "repositories",
    "repo_snapshots",
    "code_chunks",
    "jobs",
    "agent_steps",
    "patches",
    "validation_runs",
    "pull_requests",
)


def upgrade() -> None:
    op.add_column(
        "repo_snapshots", sa.Column("embedding_model", sa.String(length=100), nullable=True)
    )
    for table in UUID_TABLES:
        op.alter_column(table, "id", server_default=sa.text("gen_random_uuid()"))


def downgrade() -> None:
    for table in UUID_TABLES:
        op.alter_column(table, "id", server_default=None)
    op.drop_column("repo_snapshots", "embedding_model")
