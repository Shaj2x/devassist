"""Environment-driven settings shared by every Python service.

Each service subclasses `ServiceSettings` and adds its own fields. Values come
only from environment variables (and an optional `.env` file in development),
so the same image runs unchanged in docker-compose and Kubernetes.
"""

from __future__ import annotations

from typing import Literal

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class ServiceSettings(BaseSettings):
    model_config = SettingsConfigDict(env_file=".env", extra="ignore")

    devassist_env: Literal["development", "test", "production"] = "development"
    log_level: str = "INFO"

    database_url: str = "postgresql://devassist:devassist@localhost:5432/devassist"
    redis_url: str = "redis://localhost:6379/0"
    kafka_bootstrap_servers: str = "localhost:29092"
    kafka_consumer_group_prefix: str = "devassist"

    # Dependency checks in /readyz give up after this many seconds each.
    readiness_timeout_seconds: float = Field(default=2.0, gt=0)

    @property
    def async_database_url(self) -> str:
        """DATABASE_URL rewritten for SQLAlchemy's asyncpg driver."""
        return to_asyncpg_url(self.database_url)


def to_asyncpg_url(url: str) -> str:
    """Accept the plain `postgresql://` form used by Go services and psql, and
    return the `postgresql+asyncpg://` form SQLAlchemy needs."""
    for prefix in ("postgresql+asyncpg://", "postgresql://", "postgres://"):
        if url.startswith(prefix):
            return "postgresql+asyncpg://" + url[len(prefix) :]
    raise ValueError(f"unsupported database URL scheme: {url.split('://', 1)[0]}")
