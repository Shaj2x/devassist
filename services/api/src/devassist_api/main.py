"""FastAPI application factory.

Shared clients (DB engine, Redis, Kafka producer) are created once in the
lifespan handler and kept on `app.state`; route dependencies read them from
there (see deps.py), so tests can build an app with fakes injected.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager

from devassist_common.db.session import create_engine, create_session_factory
from devassist_common.health import check_kafka, check_postgres, check_redis
from devassist_common.kafka import EventPublisher
from devassist_common.logging import configure_logging
from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from redis.asyncio import Redis

from devassist_api.config import Settings
from devassist_api.consumers import Notifier, run_consumers
from devassist_api.github import GitHubClient
from devassist_api.health import router as health_router
from devassist_api.routers import auth, events, jobs, repos

VERSION = "0.1.0"
log = logging.getLogger(__name__)


def create_app(settings: Settings | None = None) -> FastAPI:
    settings = settings or Settings()
    configure_logging(settings.service_name, settings.log_level)
    if settings.auth_mode == "token" and not settings.token_encryption_key:
        raise ValueError("AUTH_MODE=token requires TOKEN_ENCRYPTION_KEY")

    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncIterator[None]:
        engine = create_engine(settings.database_url)
        redis = Redis.from_url(settings.redis_url)
        publisher = EventPublisher(settings.kafka_bootstrap_servers, settings.service_name)
        await publisher.start()
        app.state.sessions = create_session_factory(engine)
        app.state.redis = redis
        app.state.publisher = publisher
        app.state.readiness_checks = {
            "postgres": lambda: check_postgres(engine),
            "redis": lambda: check_redis(redis),
            "kafka": lambda: check_kafka(settings.kafka_bootstrap_servers),
        }
        stop = asyncio.Event()
        consumer_task: asyncio.Task[None] | None = None
        if settings.run_consumers:
            consumer_task = asyncio.create_task(
                run_consumers(
                    settings.kafka_bootstrap_servers,
                    settings.kafka_consumer_group_prefix,
                    Notifier(app.state.sessions, redis),
                    publisher.producer,
                    stop,
                )
            )
        try:
            yield
        finally:
            stop.set()
            if consumer_task is not None:
                with contextlib.suppress(Exception):
                    await asyncio.wait_for(consumer_task, timeout=10)
            await publisher.stop()
            await redis.aclose()
            await engine.dispose()

    app = FastAPI(title="DevAssist API", version=VERSION, lifespan=lifespan)
    app.state.settings = settings
    app.state.readiness_checks = {}
    app.state.github_factory = lambda token: GitHubClient(token, base_url=settings.github_api_url)
    app.add_middleware(
        CORSMiddleware,
        allow_origins=settings.cors_origins,
        allow_credentials=True,
        allow_methods=["*"],
        allow_headers=["*"],
    )
    app.include_router(health_router)
    app.include_router(auth.router)
    app.include_router(repos.router)
    app.include_router(jobs.router)
    app.include_router(events.router)
    return app
