from __future__ import annotations

import asyncio

from devassist_common.health import run_checks


async def ok() -> None:
    return None


async def broken() -> None:
    raise ConnectionError("refused")


async def silent_failure() -> None:
    raise RuntimeError


async def hangs() -> None:
    await asyncio.sleep(10)


async def test_run_checks_reports_each_dependency() -> None:
    results = await run_checks(
        {"db": ok, "cache": broken, "bus": hangs, "other": silent_failure}, timeout_seconds=0.05
    )
    assert results == {
        "db": "ok",
        "cache": "ConnectionError: refused",
        "bus": "timeout after 0.05s",
        "other": "RuntimeError",
    }
