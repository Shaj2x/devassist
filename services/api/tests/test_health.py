from __future__ import annotations

from collections.abc import AsyncIterator

import httpx
import pytest
from devassist_api.config import Settings
from devassist_api.main import create_app
from fastapi import FastAPI


async def ok() -> None:
    return None


async def down() -> None:
    raise ConnectionError("connection refused")


@pytest.fixture
def app() -> FastAPI:
    # The lifespan (which connects to real infra) does not run under
    # ASGITransport, so we inject fake checks directly.
    return create_app(Settings(_env_file=None))


@pytest.fixture
async def client(app: FastAPI) -> AsyncIterator[httpx.AsyncClient]:
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as c:
        yield c


async def test_healthz(client: httpx.AsyncClient) -> None:
    resp = await client.get("/healthz")
    assert resp.status_code == 200
    assert resp.json() == {"status": "ok"}


async def test_readyz_all_healthy(app: FastAPI, client: httpx.AsyncClient) -> None:
    app.state.readiness_checks = {"postgres": ok, "redis": ok, "kafka": ok}
    resp = await client.get("/readyz")
    assert resp.status_code == 200
    assert resp.json()["checks"] == {"postgres": "ok", "redis": "ok", "kafka": "ok"}


async def test_readyz_reports_failing_dependency(app: FastAPI, client: httpx.AsyncClient) -> None:
    app.state.readiness_checks = {"postgres": ok, "redis": down}
    resp = await client.get("/readyz")
    assert resp.status_code == 503
    body = resp.json()
    assert body["status"] == "unavailable"
    assert body["checks"]["redis"] == "ConnectionError: connection refused"


async def test_openapi_is_served(client: httpx.AsyncClient) -> None:
    resp = await client.get("/openapi.json")
    assert resp.status_code == 200
    assert resp.json()["info"]["title"] == "DevAssist API"
