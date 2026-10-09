"""Secrets handling: GitHub tokens encrypted at rest, opaque session tokens.

GitHub tokens are encrypted with Fernet (AES-128-CBC + HMAC) before they
touch the database. Session tokens are random; only their SHA-256 is stored
(as a Redis key), so a Redis dump does not yield usable sessions.
"""

from __future__ import annotations

import hashlib
import secrets
import uuid

from cryptography.fernet import Fernet, InvalidToken
from redis.asyncio import Redis

SESSION_PREFIX = "devassist:session:"


class TokenCipher:
    def __init__(self, key: str) -> None:
        if not key:
            raise ValueError("TOKEN_ENCRYPTION_KEY is required to store GitHub tokens")
        self._fernet = Fernet(key.encode())

    def encrypt(self, token: str) -> bytes:
        return self._fernet.encrypt(token.encode())

    def decrypt(self, data: bytes) -> str:
        try:
            return self._fernet.decrypt(data).decode()
        except InvalidToken as e:
            raise ValueError(
                "stored token cannot be decrypted (was TOKEN_ENCRYPTION_KEY rotated?)"
            ) from e


def _session_key(token: str) -> str:
    return SESSION_PREFIX + hashlib.sha256(token.encode()).hexdigest()


async def create_session(redis: Redis, user_id: uuid.UUID, ttl_seconds: int) -> str:
    token = secrets.token_urlsafe(32)
    await redis.set(_session_key(token), str(user_id), ex=ttl_seconds)
    return token


async def resolve_session(redis: Redis, token: str) -> uuid.UUID | None:
    raw = await redis.get(_session_key(token))
    if not raw:
        return None
    return uuid.UUID(raw.decode() if isinstance(raw, bytes) else raw)


async def end_session(redis: Redis, token: str) -> None:
    await redis.delete(_session_key(token))
