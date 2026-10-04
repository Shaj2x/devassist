"""FastAPI application factory.

Phase 1 exposes only health endpoints; resource routers arrive in Phase 5.
Shared clients (DB engine, Redis) are created once in the lifespan handler and
kept on `app.state`, so tests can build an app with fakes injected.
"""

from __future__ import annotations

from collections.abc import AsyncIterator
from contextlib import asynccontextmanager

from devassist_common.db.session import create_engine
from devassist_common.health import check_kafka, check_postgres, check_redis
from devassist_common.logging import configure_logging
from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from redis.asyncio import Redis

from devassist_api.config import Settings
from devassist_api.health import router as health_router

VERSION = "0.1.0"


def create_app(settings: Settings | None = None) -> FastAPI:
    settings = settings or Settings()
    configure_logging(settings.service_name, settings.log_level)

    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncIterator[None]:
        engine = create_engine(settings.database_url)
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

    app = FastAPI(title="DevAssist API", version=VERSION, lifespan=lifespan)
    app.state.settings = settings
    app.state.readiness_checks = {}
    app.add_middleware(
        CORSMiddleware,
        allow_origins=settings.cors_origins,
        allow_credentials=True,
        allow_methods=["*"],
        allow_headers=["*"],
    )
    app.include_router(health_router)
    return app
