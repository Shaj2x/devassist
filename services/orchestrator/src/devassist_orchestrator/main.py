"""Process entrypoint: a health server and the worker sharing one event loop.

uvicorn owns signal handling. When it receives SIGTERM it stops serving, and
we then signal the worker to finish its current unit of work and exit.
"""

from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from typing import Any

import uvicorn
from devassist_common.db.session import create_engine
from devassist_common.health import check_kafka, check_postgres, check_redis, run_checks
from devassist_common.logging import configure_logging
from fastapi import FastAPI, Response, status
from redis.asyncio import Redis

from devassist_orchestrator.config import Settings
from devassist_orchestrator.worker import Worker


def create_health_app(settings: Settings, worker: Worker) -> FastAPI:
    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncIterator[None]:
        engine = create_engine(settings.database_url, pool_size=2)
        redis = Redis.from_url(settings.redis_url)
        app.state.readiness_checks = {
            "postgres": lambda: check_postgres(engine),
            "redis": lambda: check_redis(redis),
            "kafka": lambda: check_kafka(settings.kafka_bootstrap_servers),
        }
        try:
            yield
        finally:
            await redis.aclose()
            await engine.dispose()

    app = FastAPI(title="DevAssist Orchestrator", lifespan=lifespan)
    app.state.readiness_checks = {}

    @app.get("/healthz")
    async def healthz(response: Response) -> dict[str, str]:
        if not worker.running:
            response.status_code = status.HTTP_503_SERVICE_UNAVAILABLE
            return {"status": "worker not running"}
        return {"status": "ok"}

    @app.get("/readyz")
    async def readyz(response: Response) -> dict[str, Any]:
        results = await run_checks(
            app.state.readiness_checks, timeout_seconds=settings.readiness_timeout_seconds
        )
        ready = all(r == "ok" for r in results.values())
        if not ready:
            response.status_code = status.HTTP_503_SERVICE_UNAVAILABLE
        return {"status": "ok" if ready else "unavailable", "checks": results}

    return app


async def serve(settings: Settings) -> None:
    worker = Worker()
    stop = asyncio.Event()
    server = uvicorn.Server(
        uvicorn.Config(
            create_health_app(settings, worker),
            host="0.0.0.0",  # noqa: S104  (container-internal bind)
            port=settings.port,
            log_config=None,
        )
    )
    worker_task = asyncio.create_task(worker.run(stop))
    try:
        await server.serve()
    finally:
        stop.set()
        await worker_task


def main() -> None:
    settings = Settings()
    configure_logging(settings.service_name, settings.log_level)
    asyncio.run(serve(settings))


if __name__ == "__main__":
    main()
