"""Liveness and readiness endpoints.

/healthz  - the process is up. Kubernetes restarts the pod if this fails.
/readyz   - every dependency answers. Kubernetes stops routing traffic (and
            docker-compose holds back dependants) while this fails.
"""

from __future__ import annotations

from typing import Any

from devassist_common.health import run_checks
from fastapi import APIRouter, Request, Response, status

router = APIRouter(tags=["health"])


@router.get("/healthz")
async def healthz() -> dict[str, str]:
    return {"status": "ok"}


@router.get("/readyz")
async def readyz(request: Request, response: Response) -> dict[str, Any]:
    settings = request.app.state.settings
    results = await run_checks(
        request.app.state.readiness_checks, timeout_seconds=settings.readiness_timeout_seconds
    )
    ready = all(result == "ok" for result in results.values())
    if not ready:
        response.status_code = status.HTTP_503_SERVICE_UNAVAILABLE
    return {"status": "ok" if ready else "unavailable", "checks": results}
