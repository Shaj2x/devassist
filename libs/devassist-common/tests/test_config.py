from __future__ import annotations

import pytest
from devassist_common.config import ServiceSettings, to_asyncpg_url


@pytest.mark.parametrize(
    "url",
    [
        "postgresql://u:p@h:5432/db",
        "postgres://u:p@h:5432/db",
        "postgresql+asyncpg://u:p@h:5432/db",
    ],
)
def test_to_asyncpg_url(url: str) -> None:
    assert to_asyncpg_url(url) == "postgresql+asyncpg://u:p@h:5432/db"


def test_to_asyncpg_url_rejects_other_schemes() -> None:
    with pytest.raises(ValueError, match="mysql"):
        to_asyncpg_url("mysql://u:p@h/db")


def test_settings_read_environment(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("DATABASE_URL", "postgresql://a:b@db:5432/x")
    monkeypatch.setenv("KAFKA_BOOTSTRAP_SERVERS", "kafka:9092")
    settings = ServiceSettings(_env_file=None)
    assert settings.async_database_url == "postgresql+asyncpg://a:b@db:5432/x"
    assert settings.kafka_bootstrap_servers == "kafka:9092"
