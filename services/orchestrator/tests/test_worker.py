from __future__ import annotations

import httpx
from devassist_orchestrator.config import Settings
from devassist_orchestrator.main import create_health_app
from devassist_orchestrator.worker import Worker


async def test_healthz_reflects_worker_state() -> None:
    worker = Worker(Settings(_env_file=None))
    app = create_health_app(Settings(_env_file=None), worker)
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        assert (await client.get("/healthz")).status_code == 503
        worker.running = True
        assert (await client.get("/healthz")).status_code == 200


async def test_readyz_with_injected_checks() -> None:
    async def ok() -> None:
        return None

    app = create_health_app(Settings(_env_file=None), Worker(Settings(_env_file=None)))
    app.state.readiness_checks = {"postgres": ok}
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        resp = await client.get("/readyz")
    assert resp.status_code == 200
    assert resp.json()["checks"] == {"postgres": "ok"}
