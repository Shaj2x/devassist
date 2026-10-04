from __future__ import annotations

from devassist_common.config import ServiceSettings
from pydantic import Field


class Settings(ServiceSettings):
    service_name: str = "api"
    # Origins allowed to call the API from a browser (the Vite dev server).
    cors_origins: list[str] = Field(
        default_factory=lambda: ["http://localhost:5173", "http://localhost:3000"]
    )
