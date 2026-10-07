from __future__ import annotations

from decimal import Decimal
from types import SimpleNamespace
from typing import Any

import pytest
from devassist_orchestrator.agents.outputs import Plan, Review
from devassist_orchestrator.llm import LLMError, LLMRefusal, LLMRequest, Message
from devassist_orchestrator.llm.anthropic_provider import FALLBACK_BETA, AnthropicProvider
from devassist_orchestrator.llm.openai_provider import OpenAIProvider
from devassist_orchestrator.llm.pricing import cost_usd, price_for
from devassist_orchestrator.llm.schema import strict_schema

REQUEST = LLMRequest(
    agent="planner",
    system="sys",
    messages=[Message("user", "hi")],
    schema={"type": "object"},
    schema_name="Plan",
    effort="high",
)


def test_strict_schema_is_self_contained_and_closed() -> None:
    schema = strict_schema(Plan)
    text = str(schema)
    assert "$ref" not in text and "$defs" not in text
    assert schema["additionalProperties"] is False
    assert set(schema["required"]) == set(schema["properties"])
    item = schema["properties"]["files_to_change"]["items"]
    assert item["additionalProperties"] is False and set(item["required"]) == {"path", "change"}
    assert strict_schema(Review)["properties"]["risk_level"]["enum"] == ["low", "medium", "high"]


def test_cost_includes_cache_pricing() -> None:
    price = price_for("claude-opus-5-5")
    # 1M input at $4, 1M output at $20, 1M cache reads at $0.20, 1M cache writes at $5.
    assert cost_usd(
        price, input_tokens=10**6, output_tokens=10**6, cache_read=10**6, cache_write=10**6
    ) == Decimal("29.2")
    assert cost_usd(price_for("unknown-model"), input_tokens=5, output_tokens=5) == 0
    assert price_for("anything", 1.0, 2.0) is not None


# --- Anthropic ------------------------------------------------------------------


def anthropic_message(**overrides: Any) -> SimpleNamespace:
    base: dict[str, Any] = dict(
        stop_reason="end_turn",
        stop_details=None,
        model="claude-opus-5-5",
        content=[
            SimpleNamespace(type="thinking", thinking=""),
            SimpleNamespace(type="text", text='{"ok": true}'),
        ],
        usage=SimpleNamespace(
            input_tokens=1000,
            output_tokens=200,
            cache_read_input_tokens=5000,
            cache_creation_input_tokens=0,
        ),
    )
    base.update(overrides)
    return SimpleNamespace(**base)


class FakeStream:
    def __init__(self, message: SimpleNamespace) -> None:
        self.message = message

    async def __aenter__(self) -> FakeStream:
        return self

    async def __aexit__(self, *exc: object) -> None:
        return None

    async def get_final_message(self) -> SimpleNamespace:
        return self.message


class FakeAnthropic:
    def __init__(self, message: SimpleNamespace) -> None:
        self.params: dict[str, Any] = {}
        outer = self

        class Messages:
            def stream(self, **params: Any) -> FakeStream:
                outer.params = params
                return FakeStream(message)

        self.beta = SimpleNamespace(messages=Messages())


def provider(message: SimpleNamespace, **kwargs: Any) -> tuple[AnthropicProvider, FakeAnthropic]:
    fake = FakeAnthropic(message)
    return AnthropicProvider(
        "claude-opus-5-5", price=price_for("claude-opus-5-5"), client=fake, **kwargs
    ), fake


async def test_anthropic_request_shape_and_usage() -> None:
    p, fake = provider(anthropic_message())
    resp = await p.complete(REQUEST)

    params = fake.params
    assert params["model"] == "claude-opus-5-5"
    assert params["output_config"] == {
        "format": {"type": "json_schema", "schema": {"type": "object"}},
        "effort": "high",
    }
    assert params["system"][0]["cache_control"] == {"type": "ephemeral"}
    assert params["betas"] == [FALLBACK_BETA] and params["fallbacks"] == "default"
    assert "thinking" not in params  # adaptive by default on current models
    assert resp.data == {"ok": True}
    assert (resp.input_tokens, resp.output_tokens, resp.cache_read_tokens) == (1000, 200, 5000)
    # 1000*4 + 200*20 + 5000*0.2 per million
    assert resp.cost_usd == Decimal("0.009")


async def test_anthropic_fallback_can_be_disabled_and_effort_skipped_for_haiku() -> None:
    fake = FakeAnthropic(anthropic_message())
    p = AnthropicProvider("claude-haiku-4-5", price=None, client=fake, refusal_fallback=False)
    await p.complete(REQUEST)
    assert "fallbacks" not in fake.params and "effort" not in fake.params["output_config"]


async def test_anthropic_refusal_and_truncation() -> None:
    refused = anthropic_message(
        stop_reason="refusal", stop_details=SimpleNamespace(category="cyber")
    )
    with pytest.raises(LLMRefusal, match="cyber"):
        await provider(refused)[0].complete(REQUEST)
    with pytest.raises(LLMError, match="max_tokens"):
        await provider(anthropic_message(stop_reason="max_tokens"))[0].complete(REQUEST)


# --- OpenAI ---------------------------------------------------------------------


class FakeOpenAI:
    def __init__(self, completion: SimpleNamespace) -> None:
        self.params: dict[str, Any] = {}
        outer = self

        class Completions:
            async def create(self, **params: Any) -> SimpleNamespace:
                outer.params = params
                return completion

        self.chat = SimpleNamespace(completions=Completions())


async def test_openai_request_shape_and_cached_tokens() -> None:
    completion = SimpleNamespace(
        model="gpt-5",
        choices=[
            SimpleNamespace(
                finish_reason="stop", message=SimpleNamespace(content='{"a": 1}', refusal=None)
            )
        ],
        usage=SimpleNamespace(
            prompt_tokens=1000,
            completion_tokens=100,
            prompt_tokens_details=SimpleNamespace(cached_tokens=400),
        ),
    )
    fake = FakeOpenAI(completion)
    p = OpenAIProvider("gpt-5", price=price_for("gpt-5"), client=fake)
    resp = await p.complete(REQUEST)

    assert fake.params["messages"][0] == {"role": "system", "content": "sys"}
    assert fake.params["response_format"]["json_schema"]["strict"] is True
    assert (resp.input_tokens, resp.cache_read_tokens) == (600, 400)
    assert resp.data == {"a": 1}
