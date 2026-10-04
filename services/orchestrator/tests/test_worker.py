from __future__ import annotations

import asyncio

import httpx
from devassist_orchestrator.config import Settings
from devassist_orchestrator.main import create_health_app
from devassist_orchestrator.worker import Worker


async def test_worker_runs_until_stopped() -> None:
    worker = Worker(idle_interval=0.01)
    stop = asyncio.Event()
    task = asyncio.create_task(worker.run(stop))
    await asyncio.sleep(0.03)
    assert worker.running

    stop.set()
    await asyncio.wait_for(task, timeout=1)
    assert not worker.running


async def test_healthz_reflects_worker_state() -> None:
    worker = Worker(idle_interval=0.01)
    app = create_health_app(Settings(_env_file=None), worker)
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        assert (await client.get("/healthz")).status_code == 503

        stop = asyncio.Event()
        task = asyncio.create_task(worker.run(stop))
        await asyncio.sleep(0.02)
        assert (await client.get("/healthz")).status_code == 200

        stop.set()
        await task


async def test_readyz_with_injected_checks() -> None:
    async def ok() -> None:
        return None

    app = create_health_app(Settings(_env_file=None), Worker())
    app.state.readiness_checks = {"postgres": ok}
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        resp = await client.get("/readyz")
    assert resp.status_code == 200
    assert resp.json()["checks"] == {"postgres": "ok"}
