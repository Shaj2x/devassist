"""OpenAI chat completions, behind the same interface.

Structured output via `response_format` with a strict JSON schema. The SDK
handles transport retries and timeouts. OpenAI reports cached prompt tokens
inside prompt_tokens, so they are split out before pricing.
"""

from __future__ import annotations

import json
import time
from typing import Any

import openai

from devassist_orchestrator.llm.base import LLMError, LLMRefusal, LLMRequest, LLMResponse
from devassist_orchestrator.llm.pricing import Price, cost_usd


class OpenAIProvider:
    name = "openai"

    def __init__(
        self,
        model: str,
        *,
        price: Price | None,
        api_key: str | None = None,
        timeout_seconds: float = 180.0,
        max_retries: int = 3,
        max_tokens: int = 32000,
        client: Any = None,
    ) -> None:
        self.model = model
        self.price = price
        self.max_tokens = max_tokens
        self.client = client or openai.AsyncOpenAI(
            api_key=api_key or None, timeout=timeout_seconds, max_retries=max_retries
        )

    def build_params(self, request: LLMRequest) -> dict[str, Any]:
        messages: list[dict[str, str]] = [{"role": "system", "content": request.system}]
        messages += [{"role": m.role, "content": m.content} for m in request.messages]
        return {
            "model": self.model,
            "messages": messages,
            "max_completion_tokens": request.max_tokens or self.max_tokens,
            "response_format": {
                "type": "json_schema",
                "json_schema": {
                    "name": request.schema_name,
                    "schema": request.schema,
                    "strict": True,
                },
            },
        }

    async def complete(self, request: LLMRequest) -> LLMResponse:
        started = time.monotonic()
        try:
            completion = await self.client.chat.completions.create(**self.build_params(request))
        except openai.RateLimitError as e:
            raise LLMError(f"rate limited after retries: {e}", retryable=True) from e
        except openai.APIStatusError as e:
            raise LLMError(
                f"OpenAI API error {e.status_code}: {e.message}", retryable=e.status_code >= 500
            ) from e
        except openai.APIConnectionError as e:
            raise LLMError(f"could not reach the OpenAI API: {e}", retryable=True) from e

        choice = completion.choices[0]
        if getattr(choice.message, "refusal", None):
            raise LLMRefusal(f"model declined the request: {choice.message.refusal}")
        if choice.finish_reason == "length":
            raise LLMError("response hit the token limit before the JSON was complete")
        text = choice.message.content or ""
        try:
            data = json.loads(text)
        except json.JSONDecodeError as e:
            raise LLMError(f"response was not valid JSON: {e}") from e

        usage = completion.usage
        prompt = usage.prompt_tokens if usage else 0
        cached = (
            (usage.prompt_tokens_details.cached_tokens or 0)
            if usage and usage.prompt_tokens_details
            else 0
        )
        output = usage.completion_tokens if usage else 0
        return LLMResponse(
            text=text,
            data=data,
            provider=self.name,
            model=completion.model,
            input_tokens=prompt - cached,
            output_tokens=output,
            cache_read_tokens=cached,
            cost_usd=cost_usd(
                self.price, input_tokens=prompt - cached, output_tokens=output, cache_read=cached
            ),
            latency_ms=int((time.monotonic() - started) * 1000),
            stop_reason=choice.finish_reason,
            request_id=getattr(completion, "_request_id", None),
        )

    async def aclose(self) -> None:
        await self.client.close()
