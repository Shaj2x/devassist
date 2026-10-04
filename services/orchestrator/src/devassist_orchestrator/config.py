from __future__ import annotations

from devassist_common.config import ServiceSettings


class Settings(ServiceSettings):
    service_name: str = "orchestrator"
    # Port for the health/metrics HTTP server. The worker itself has no API.
    port: int = 8001
