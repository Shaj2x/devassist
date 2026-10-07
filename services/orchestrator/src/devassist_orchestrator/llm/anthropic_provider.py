"""Claude via the official Anthropic SDK.

- Streaming request + `get_final_message()`: agents can emit whole files, and
  streaming keeps long generations clear of HTTP timeouts.
- Structured output through `output_config.format` (JSON schema), so the
  response text is guaranteed to parse.
- `output_config.effort` per agent; thinking is left at the model default
  (adaptive on current models).
- The stable system prompt is marked for prompt caching.
- On a safety refusal the API can retry on a fallback model it chooses
  (`fallbacks="default"`); a refusal that survives that raises LLMRefusal.
- Transport retries (429, 5xx, connection errors) and the per-attempt timeout
  are the SDK's, configured from LLM_MAX_RETRIES / LLM_TIMEOUT_SECONDS.
"""

from __future__ import annotations

import json
import time
from typing import Any

import anthropic

from devassist_orchestrator.llm.base import LLMError, LLMRefusal, LLMRequest, LLMResponse
from devassist_orchestrator.llm.pricing import Price, cost_usd

FALLBACK_BETA = "server-side-fallback-2026-07-01"

# Models that reject `output_config.effort`.
_NO_EFFORT_PREFIXES = ("claude-haiku-4-5", "claude-sonnet-4-5")


class AnthropicProvider:
    name = "anthropic"

    def __init__(
        self,
        model: str,
        *,
        price: Price | None,
        api_key: str | None = None,
        timeout_seconds: float = 180.0,
        max_retries: int = 3,
        max_tokens: int = 32000,
        refusal_fallback: bool = True,
        client: Any = None,
    ) -> None:
        self.model = model
        self.price = price
        self.max_tokens = max_tokens
        self.refusal_fallback = refusal_fallback
        # api_key=None lets the SDK resolve credentials (env var or profile).
        self.client = client or anthropic.AsyncAnthropic(
            api_key=api_key or None, timeout=timeout_seconds, max_retries=max_retries
        )

    def build_params(self, request: LLMRequest) -> dict[str, Any]:
        output_config: dict[str, Any] = {
            "format": {"type": "json_schema", "schema": request.schema}
        }
        if not self.model.startswith(_NO_EFFORT_PREFIXES):
            output_config["effort"] = request.effort
        params: dict[str, Any] = {
            "model": self.model,
            "max_tokens": request.max_tokens or self.max_tokens,
            # The system prompt is identical across calls for an agent: cache it.
            "system": [
                {"type": "text", "text": request.system, "cache_control": {"type": "ephemeral"}}
            ],
            "messages": [{"role": m.role, "content": m.content} for m in request.messages],
            "output_config": output_config,
        }
        if self.refusal_fallback:
            params["betas"] = [FALLBACK_BETA]
            params["fallbacks"] = "default"
        return params

    async def complete(self, request: LLMRequest) -> LLMResponse:
        started = time.monotonic()
        try:
            async with self.client.beta.messages.stream(**self.build_params(request)) as stream:
                message = await stream.get_final_message()
        except anthropic.RateLimitError as e:
            raise LLMError(f"rate limited after retries: {e.message}", retryable=True) from e
        except anthropic.APIStatusError as e:
            raise LLMError(
                f"Anthropic API error {e.status_code}: {e.message}", retryable=e.status_code >= 500
            ) from e
        except anthropic.APIConnectionError as e:  # includes timeouts
            raise LLMError(f"could not reach the Anthropic API: {e}", retryable=True) from e
        return self.parse_message(message, latency_ms=int((time.monotonic() - started) * 1000))

    def parse_message(self, message: Any, *, latency_ms: int) -> LLMResponse:
        if message.stop_reason == "refusal":
            details = getattr(message, "stop_details", None)
            category = getattr(details, "category", None) if details else None
            raise LLMRefusal(f"model declined the request (category: {category or 'unspecified'})")
        if message.stop_reason == "max_tokens":
            raise LLMError("response hit max_tokens before the JSON was complete")

        text = next((b.text for b in message.content if b.type == "text"), "")
        try:
            data = json.loads(text)
        except json.JSONDecodeError as e:
            raise LLMError(f"response was not valid JSON: {e}") from e

        usage = message.usage
        input_tokens = usage.input_tokens or 0
        cache_read = usage.cache_read_input_tokens or 0
        cache_write = usage.cache_creation_input_tokens or 0
        return LLMResponse(
            text=text,
            data=data,
            provider=self.name,
            model=message.model,  # may differ from self.model if a fallback served it
            input_tokens=input_tokens,
            output_tokens=usage.output_tokens or 0,
            cache_read_tokens=cache_read,
            cache_write_tokens=cache_write,
            cost_usd=cost_usd(
                self.price,
                input_tokens=input_tokens,
                output_tokens=usage.output_tokens or 0,
                cache_read=cache_read,
                cache_write=cache_write,
            ),
            latency_ms=latency_ms,
            stop_reason=message.stop_reason,
            request_id=getattr(message, "_request_id", None),
        )

    async def aclose(self) -> None:
        await self.client.close()
