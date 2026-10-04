"""Integration tests: run against the Postgres from `make up`.

They migrate a throwaway database (not the dev database) from scratch, check
there is no drift between the models and the migrations, and prove pgvector
search works on the resulting schema.
"""

from __future__ import annotations

import asyncio
import os
import subprocess
import uuid
from collections.abc import AsyncIterator, Iterator
from pathlib import Path

import pytest
from devassist_common.config import to_asyncpg_url
from devassist_common.db import (
    EMBEDDING_DIMENSIONS,
    CodeChunk,
    Repository,
    RepoSnapshot,
    User,
)
from sqlalchemy import make_url, select, text
from sqlalchemy.ext.asyncio import AsyncEngine, AsyncSession, create_async_engine

pytestmark = pytest.mark.integration

API_DIR = Path(__file__).resolve().parents[1]
BASE_URL = os.environ.get(
    "DATABASE_URL", "postgresql://devassist:devassist@localhost:5432/devassist"
)
TEST_DB = "devassist_migration_test"


def alembic(*args: str, url: str) -> str:
    result = subprocess.run(  # noqa: S603
        ["alembic", *args],  # noqa: S607
        cwd=API_DIR,
        env={**os.environ, "DATABASE_URL": url},
        capture_output=True,
        text=True,
        check=True,
    )
    return result.stdout + result.stderr


async def _recreate_test_db(*, drop_only: bool = False) -> None:
    admin = create_async_engine(to_asyncpg_url(BASE_URL), isolation_level="AUTOCOMMIT")
    try:
        async with admin.connect() as conn:
            await conn.execute(text(f"DROP DATABASE IF EXISTS {TEST_DB} WITH (FORCE)"))
            if not drop_only:
                await conn.execute(text(f"CREATE DATABASE {TEST_DB}"))
    finally:
        await admin.dispose()


@pytest.fixture(scope="module")
def test_db_url() -> Iterator[str]:
    asyncio.run(_recreate_test_db())
    yield make_url(BASE_URL).set(database=TEST_DB).render_as_string(hide_password=False)
    asyncio.run(_recreate_test_db(drop_only=True))


@pytest.fixture
async def engine(test_db_url: str) -> AsyncIterator[AsyncEngine]:
    alembic("upgrade", "head", url=test_db_url)
    eng = create_async_engine(to_asyncpg_url(test_db_url))
    yield eng
    await eng.dispose()


def test_upgrade_downgrade_round_trip(test_db_url: str) -> None:
    alembic("upgrade", "head", url=test_db_url)
    alembic("downgrade", "base", url=test_db_url)
    alembic("upgrade", "head", url=test_db_url)


def test_models_match_migrations(test_db_url: str) -> None:
    alembic("upgrade", "head", url=test_db_url)
    assert "No new upgrade operations detected" in alembic("check", url=test_db_url)


async def test_vector_similarity_search(engine: AsyncEngine) -> None:
    def unit(index: int) -> list[float]:
        vec = [0.0] * EMBEDDING_DIMENSIONS
        vec[index] = 1.0
        return vec

    async with AsyncSession(engine, expire_on_commit=False) as session:
        user = User(github_login=f"tester-{uuid.uuid4().hex[:8]}")
        repo = Repository(owner=user, full_name="t/repo", clone_url="file:///tmp/repo")
        snap = RepoSnapshot(repository=repo, commit_sha="abc123")
        session.add_all([user, repo, snap])
        await session.flush()
        for i, name in enumerate(["parse_date", "is_leap", "format_money"]):
            session.add(
                CodeChunk(
                    snapshot_id=snap.id,
                    repo_id=repo.id,
                    file_path=f"src/{name}.py",
                    symbol_name=name,
                    symbol_kind="function",
                    start_line=1,
                    end_line=5,
                    content=f"def {name}(): ...",
                    content_hash=name,
                    embedding=unit(i),
                )
            )
        await session.commit()

        query = [0.0] * EMBEDDING_DIMENSIONS
        query[1], query[0] = 0.9, 0.1  # closest to is_leap, then parse_date
        rows = (
            await session.execute(
                select(CodeChunk.symbol_name)
                .where(CodeChunk.repo_id == repo.id)
                .order_by(CodeChunk.embedding.cosine_distance(query))
                .limit(2)
            )
        ).scalars()
        assert list(rows) == ["is_leap", "parse_date"]


async def test_status_check_constraint_rejects_unknown_values(engine: AsyncEngine) -> None:
    async with engine.connect() as conn:
        user_id, repo_id = uuid.uuid4(), uuid.uuid4()
        await conn.execute(
            text("INSERT INTO users (id, github_login) VALUES (:id, :login)"),
            {"id": user_id, "login": f"u-{user_id.hex[:8]}"},
        )
        await conn.execute(
            text(
                "INSERT INTO repositories (id, owner_id, full_name, clone_url, default_branch)"
                " VALUES (:id, :owner, 'a/b', 'x', 'main')"
            ),
            {"id": repo_id, "owner": user_id},
        )
        with pytest.raises(Exception, match="ck_repo_snapshots_snapshot_status"):
            await conn.execute(
                text(
                    "INSERT INTO repo_snapshots (id, repo_id, commit_sha, status)"
                    " VALUES (:id, :repo, 'sha', 'bogus')"
                ),
                {"id": uuid.uuid4(), "repo": repo_id},
            )
