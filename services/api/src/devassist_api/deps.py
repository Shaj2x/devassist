"""Request dependencies: database session, Redis, event publisher, the caller.

The lifespan handler in `main.py` puts long-lived clients on `app.state`;
these functions hand them to route handlers. Tests swap in their own
objects by setting the same attributes.
"""

from __future__ import annotations

import uuid
from collections.abc import AsyncIterator, Callable
from dataclasses import dataclass
from typing import Annotated, Protocol

from devassist_common.db import User
from devassist_common.events import EventType
from devassist_common.events import _Payload as Payload
from fastapi import Depends, HTTPException, Request, status
from redis.asyncio import Redis
from sqlalchemy import select
from sqlalchemy.dialects.postgresql import insert
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker

from devassist_api.config import Settings
from devassist_api.github import GitHubClient
from devassist_api.security import TokenCipher, resolve_session

DEV_LOGIN = "demo"  # the indexer CLI registers demo repos under this user too


class Publisher(Protocol):
    async def publish(
        self, event_type: EventType, payload: Payload, *, trace_id: str
    ) -> object: ...


def get_settings(request: Request) -> Settings:
    settings: Settings = request.app.state.settings
    return settings


async def get_db(request: Request) -> AsyncIterator[AsyncSession]:
    sessions: async_sessionmaker[AsyncSession] = request.app.state.sessions
    async with sessions() as session:
        yield session


def get_redis(request: Request) -> Redis:
    redis: Redis = request.app.state.redis
    return redis


def get_publisher(request: Request) -> Publisher:
    publisher: Publisher = request.app.state.publisher
    return publisher


SettingsDep = Annotated[Settings, Depends(get_settings)]
DbDep = Annotated[AsyncSession, Depends(get_db)]
RedisDep = Annotated[Redis, Depends(get_redis)]
PublisherDep = Annotated[Publisher, Depends(get_publisher)]


@dataclass(frozen=True)
class CurrentUser:
    id: uuid.UUID
    login: str
    avatar_url: str | None
    # The token GitHub calls are made with; "" means unauthenticated
    # (public repositories only, low rate limit).
    github_token: str
    session_token: str | None = None


def bearer_token(request: Request) -> str | None:
    header = request.headers.get("authorization", "")
    if header.lower().startswith("bearer "):
        return header[7:].strip() or None
    # EventSource cannot set headers, so SSE endpoints accept a query param.
    return request.query_params.get("access_token") or None


async def _dev_user(db: AsyncSession, settings: Settings) -> CurrentUser:
    stmt = (
        insert(User)
        .values(github_login=DEV_LOGIN)
        .on_conflict_do_update(index_elements=[User.github_login], set_={"github_login": DEV_LOGIN})
        .returning(User.id, User.avatar_url)
    )
    row = (await db.execute(stmt)).one()
    await db.commit()
    return CurrentUser(
        id=row.id, login=DEV_LOGIN, avatar_url=row.avatar_url, github_token=settings.github_token
    )


async def current_user(
    request: Request, db: DbDep, redis: RedisDep, settings: SettingsDep
) -> CurrentUser:
    if settings.auth_mode == "dev":
        return await _dev_user(db, settings)

    unauthorized = HTTPException(
        status.HTTP_401_UNAUTHORIZED,
        "sign in with a GitHub token first",
        headers={"WWW-Authenticate": "Bearer"},
    )
    token = bearer_token(request)
    if token is None:
        raise unauthorized
    user_id = await resolve_session(redis, token)
    if user_id is None:
        raise unauthorized
    user = await db.scalar(select(User).where(User.id == user_id))
    if user is None:
        raise unauthorized
    github_token = ""
    if user.github_token_encrypted:
        github_token = TokenCipher(settings.token_encryption_key).decrypt(
            user.github_token_encrypted
        )
    return CurrentUser(
        id=user.id,
        login=user.github_login,
        avatar_url=user.avatar_url,
        github_token=github_token,
        session_token=token,
    )


UserDep = Annotated[CurrentUser, Depends(current_user)]


GitHubFactory = Callable[[str], GitHubClient]


def get_github(request: Request) -> GitHubFactory:
    """Returns a function that builds a GitHub client for a token."""
    factory: GitHubFactory = request.app.state.github_factory
    return factory


GitHubDep = Annotated[GitHubFactory, Depends(get_github)]


def github_web_url(api_url: str) -> str:
    """https://api.github.com -> https://github.com; GHE .../api/v3 -> ..."""
    api_url = api_url.rstrip("/")
    if api_url == "https://api.github.com":
        return "https://github.com"
    return api_url.removesuffix("/api/v3")
