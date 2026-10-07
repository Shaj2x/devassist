"""The provider interface every agent talks to.

Agents never import an SDK. They build an `LLMRequest` (system prompt,
messages, the JSON schema their answer must match, an effort hint) and get an
`LLMResponse` back with the parsed JSON plus the accounting the dashboard
shows: tokens, cost, latency, model.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from decimal import Decimal
from typing import Any, Literal, Protocol

Effort = Literal["low", "medium", "high", "xhigh", "max"]


@dataclass(frozen=True)
class Message:
    role: Literal["user", "assistant"]
    content: str


@dataclass(frozen=True)
class LLMRequest:
    agent: str  # which agent is asking; used for logging and the mock provider
    system: str
    messages: list[Message]
    # JSON Schema the response must satisfy (strict: every property required,
    # no additional properties). Providers enforce it natively.
    schema: dict[str, Any]
    schema_name: str
    effort: Effort = "high"
    max_tokens: int | None = None


@dataclass
class LLMResponse:
    text: str
    data: dict[str, Any]
    provider: str
    model: str
    input_tokens: int = 0
    output_tokens: int = 0
    cache_read_tokens: int = 0
    cache_write_tokens: int = 0
    cost_usd: Decimal = Decimal(0)
    latency_ms: int = 0
    stop_reason: str | None = None
    request_id: str | None = None
    extra: dict[str, Any] = field(default_factory=dict)


class LLMError(Exception):
    """A call failed after the SDK's own retries, or returned unusable output."""

    def __init__(self, message: str, *, retryable: bool = False) -> None:
        super().__init__(message)
        self.retryable = retryable


class LLMRefusal(LLMError):
    """The model declined the request on safety grounds."""


class LLMProvider(Protocol):
    name: str
    model: str

    async def complete(self, request: LLMRequest) -> LLMResponse: ...

    async def aclose(self) -> None: ...
