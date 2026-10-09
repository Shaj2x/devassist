from __future__ import annotations

from typing import Literal

from devassist_common.config import ServiceSettings
from pydantic import Field


class Settings(ServiceSettings):
    service_name: str = "api"
    # Origins allowed to call the API from a browser (the Vite dev server).
    cors_origins: list[str] = Field(
        default_factory=lambda: ["http://localhost:5173", "http://localhost:3000"]
    )

    # dev:   no login; every request acts as a local "demo" user, and GitHub
    #        calls use GITHUB_TOKEN if set. For running on your own machine.
    # token: users sign in with a GitHub personal access token; requests carry
    #        an opaque session token. Use this for anything others can reach.
    auth_mode: Literal["dev", "token"] = "dev"
    session_ttl_seconds: int = 7 * 24 * 3600
    # Fernet key for GitHub tokens at rest. Required in token mode.
    token_encryption_key: str = ""

    github_api_url: str = "https://api.github.com"
    github_token: str = ""  # dev mode / fallback token

    # Allow registering non-GitHub clone URLs (file://, other hosts). On in
    # dev mode for the bundled sample repos; off by default in token mode.
    allow_local_repos: bool | None = None

    default_max_iterations: int = Field(default=3, ge=1, le=10)
    # Background Kafka consumers (repo.indexed, job.completed). Tests turn
    # this off.
    run_consumers: bool = True

    @property
    def local_repos_allowed(self) -> bool:
        return (
            self.allow_local_repos
            if self.allow_local_repos is not None
            else self.auth_mode == "dev"
        )
