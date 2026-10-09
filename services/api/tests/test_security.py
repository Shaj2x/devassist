from __future__ import annotations

import uuid
from typing import Any

import pytest
from cryptography.fernet import Fernet
from devassist_api.config import Settings
from devassist_api.main import create_app
from devassist_api.security import (
    SESSION_PREFIX,
    TokenCipher,
    create_session,
    end_session,
    resolve_session,
)


class FakeRedis:
    def __init__(self) -> None:
        self.data: dict[str, bytes] = {}
        self.ttl: dict[str, int] = {}

    async def set(self, key: str, value: str, ex: int | None = None) -> None:
        self.data[key] = value.encode()
        if ex:
            self.ttl[key] = ex

    async def get(self, key: str) -> bytes | None:
        return self.data.get(key)

    async def delete(self, key: str) -> None:
        self.data.pop(key, None)


def test_cipher_round_trip() -> None:
    cipher = TokenCipher(Fernet.generate_key().decode())
    encrypted = cipher.encrypt("ghp_secret")
    assert b"ghp_secret" not in encrypted
    assert cipher.decrypt(encrypted) == "ghp_secret"


def test_cipher_rejects_other_key() -> None:
    encrypted = TokenCipher(Fernet.generate_key().decode()).encrypt("t")
    with pytest.raises(ValueError, match="rotated"):
        TokenCipher(Fernet.generate_key().decode()).decrypt(encrypted)


def test_cipher_requires_key() -> None:
    with pytest.raises(ValueError):
        TokenCipher("")


async def test_sessions_store_only_a_hash() -> None:
    redis: Any = FakeRedis()
    user_id = uuid.uuid4()
    token = await create_session(redis, user_id, ttl_seconds=60)
    (key,) = redis.data
    assert key.startswith(SESSION_PREFIX) and token not in key
    assert redis.ttl[key] == 60
    assert await resolve_session(redis, token) == user_id
    assert await resolve_session(redis, "forged") is None
    await end_session(redis, token)
    assert await resolve_session(redis, token) is None


def test_token_mode_requires_encryption_key() -> None:
    with pytest.raises(ValueError, match="TOKEN_ENCRYPTION_KEY"):
        create_app(Settings(_env_file=None, auth_mode="token"))


def test_local_repos_default_follows_auth_mode() -> None:
    key = Fernet.generate_key().decode()
    assert Settings(_env_file=None).local_repos_allowed
    token = Settings(_env_file=None, auth_mode="token", token_encryption_key=key)
    assert not token.local_repos_allowed
