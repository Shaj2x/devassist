from __future__ import annotations

from typing import Literal

from devassist_common.config import ServiceSettings
from pydantic import Field


class Settings(ServiceSettings):
    service_name: str = "orchestrator"
    # Port for the health HTTP server. The worker itself has no API.
    port: int = 8001

    # --- LLM ---------------------------------------------------------------
    llm_provider: Literal["anthropic", "openai", "mock"] = "mock"
    # Empty = provider default (anthropic: claude-opus-5-5, openai: gpt-5).
    llm_model: str = ""
    anthropic_api_key: str = ""
    openai_api_key: str = ""
    # Per-attempt timeout; the SDKs retry 408/409/429/5xx and connection
    # errors with exponential backoff up to llm_max_retries times.
    llm_timeout_seconds: float = Field(default=180.0, gt=0)
    llm_max_retries: int = Field(default=3, ge=0)
    llm_max_tokens: int = Field(default=32000, gt=0)
    # Anthropic only: on a safety refusal, let the API retry on a fallback
    # model it picks for the refusal category.
    llm_refusal_fallback: bool = True
    # Mock provider: JSON script of canned agent responses.
    llm_mock_script: str = ""
    # Price overrides (USD per million tokens) for models not in the table.
    llm_price_input_per_mtok: float | None = None
    llm_price_output_per_mtok: float | None = None

    # --- Agent loop -----------------------------------------------------------
    max_iterations: int = Field(default=3, ge=1, le=10)
    # Hard spend ceiling per job; the loop stops before exceeding it.
    job_budget_usd: float = Field(default=2.0, gt=0)
    retrieval_top_k: int = Field(default=8, ge=1, le=50)
    # Files larger than this are not shown to agents in full.
    max_file_chars: int = Field(default=60_000, gt=0)

    # --- Collaborators --------------------------------------------------------
    indexer_url: str = "http://localhost:8080"
    sandbox_runner_url: str = "http://localhost:8081"
    sandbox_timeout_seconds: float = Field(default=600.0, gt=0)
    work_dir: str = "/tmp/devassist-orchestrator"  # noqa: S108  (per-job subdirs via mkdtemp)
    github_token: str = ""

    @property
    def model(self) -> str:
        if self.llm_model:
            return self.llm_model
        return {"anthropic": "claude-opus-5-5", "openai": "gpt-5", "mock": "mock"}[
            self.llm_provider
        ]
