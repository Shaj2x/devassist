"""Readiness probes for the shared infrastructure.

Each check returns `None` when the dependency is healthy or a short error
string when it is not. `/readyz` endpoints run them concurrently with a
timeout so a hung dependency cannot hang the probe.
"""

from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable

from aiokafka.admin import AIOKafkaAdminClient
from redis.asyncio import Redis
from sqlalchemy import text
from sqlalchemy.ext.asyncio import AsyncEngine

from devassist_common.events import TOPICS

Check = Callable[[], Awaitable[None]]


async def check_postgres(engine: AsyncEngine) -> None:
    async with engine.connect() as conn:
        await conn.execute(text("SELECT 1"))


async def check_redis(redis: Redis) -> None:
    await redis.ping()


async def check_kafka(bootstrap_servers: str) -> None:
    """Kafka is ready when the broker answers and every DevAssist topic exists."""
    admin = AIOKafkaAdminClient(bootstrap_servers=bootstrap_servers, request_timeout_ms=2000)
    await admin.start()
    try:
        topics = set(await admin.list_topics())
    finally:
        await admin.close()
    missing = sorted(set(TOPICS) - topics)
    if missing:
        raise RuntimeError(f"missing topics: {', '.join(missing)}")


async def run_checks(checks: dict[str, Check], timeout_seconds: float) -> dict[str, str]:
    """Run checks concurrently. Returns {name: "ok" | error message}."""

    async def run_one(check: Check) -> str:
        try:
            async with asyncio.timeout(timeout_seconds):
                await check()
        except TimeoutError:
            return f"timeout after {timeout_seconds}s"
        except Exception as exc:
            detail = str(exc)
            return f"{type(exc).__name__}: {detail}" if detail else type(exc).__name__
        return "ok"

    names = list(checks)
    results = await asyncio.gather(*(run_one(checks[n]) for n in names))
    return dict(zip(names, results, strict=True))
