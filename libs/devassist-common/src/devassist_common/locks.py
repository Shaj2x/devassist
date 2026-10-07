"""Redis distributed lock (the Python twin of libs/gocommon/lock).

SET key token NX PX ttl to acquire; release and extend run Lua scripts that
act only if the token still matches, so an expired holder can never delete
or extend someone else's lock.
"""

from __future__ import annotations

import asyncio
import contextlib
import secrets
from collections.abc import AsyncIterator

from redis.asyncio import Redis

KEY_PREFIX = "devassist:lock:"

_RELEASE = """
if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) end
return 0
"""
_EXTEND = """
if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("PEXPIRE", KEYS[1], ARGV[2]) end
return 0
"""


class LockNotAcquired(Exception):
    pass


@contextlib.asynccontextmanager
async def redis_lock(redis: Redis, name: str, ttl_seconds: float = 120.0) -> AsyncIterator[None]:
    """Hold `name` for the duration of the block, renewing it every ttl/3.
    Raises LockNotAcquired if someone else holds it."""
    key, token, ttl_ms = KEY_PREFIX + name, secrets.token_hex(16), int(ttl_seconds * 1000)
    if not await redis.set(key, token, nx=True, px=ttl_ms):
        raise LockNotAcquired(name)

    async def keep_alive() -> None:
        while True:
            await asyncio.sleep(ttl_seconds / 3)
            await redis.eval(_EXTEND, 1, key, token, ttl_ms)

    renewer = asyncio.create_task(keep_alive())
    try:
        yield
    finally:
        renewer.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await renewer
        await redis.eval(_RELEASE, 1, key, token)
