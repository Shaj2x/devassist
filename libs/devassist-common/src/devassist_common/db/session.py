"""Async engine and session factory."""

from __future__ import annotations

from sqlalchemy.ext.asyncio import (
    AsyncEngine,
    AsyncSession,
    async_sessionmaker,
    create_async_engine,
)

from devassist_common.config import to_asyncpg_url


def create_engine(database_url: str, *, pool_size: int = 5) -> AsyncEngine:
    return create_async_engine(
        to_asyncpg_url(database_url),
        pool_size=pool_size,
        pool_pre_ping=True,
    )


def create_session_factory(engine: AsyncEngine) -> async_sessionmaker[AsyncSession]:
    return async_sessionmaker(engine, expire_on_commit=False)
