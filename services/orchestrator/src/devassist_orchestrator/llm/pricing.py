"""Token prices for cost tracking (USD per million tokens).

Anthropic prices are from the published price list; cache writes (5-minute
TTL) cost 1.25x input. OpenAI entries are best-effort defaults; set
LLM_PRICE_INPUT_PER_MTOK / LLM_PRICE_OUTPUT_PER_MTOK to override for any model.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal


@dataclass(frozen=True)
class Price:
    input: Decimal
    output: Decimal
    cache_read: Decimal
    cache_write: Decimal


def _anthropic(inp: str, out: str, cache_read: str) -> Price:
    i = Decimal(inp)
    return Price(
        input=i,
        output=Decimal(out),
        cache_read=Decimal(cache_read),
        cache_write=i * Decimal("1.25"),
    )


def _openai(inp: str, out: str, cached: str) -> Price:
    i = Decimal(inp)
    return Price(input=i, output=Decimal(out), cache_read=Decimal(cached), cache_write=i)


PRICES: dict[str, Price] = {
    "claude-fable-5-1": _anthropic("10", "50", "0.25"),
    "claude-opus-5-5": _anthropic("4", "20", "0.20"),
    "claude-opus-5": _anthropic("5", "25", "0.50"),
    "claude-sonnet-5-5": _anthropic("2", "10", "0.20"),
    "claude-sonnet-5": _anthropic("2", "10", "0.20"),
    "claude-haiku-4-5": _anthropic("1", "5", "0.10"),
    "gpt-5": _openai("1.25", "10", "0.125"),
    "gpt-5-mini": _openai("0.25", "2", "0.025"),
    "gpt-4.1": _openai("2", "8", "0.5"),
    "mock": Price(Decimal(0), Decimal(0), Decimal(0), Decimal(0)),
}

MILLION = Decimal(1_000_000)


def price_for(
    model: str, override_in: float | None = None, override_out: float | None = None
) -> Price | None:
    if override_in is not None and override_out is not None:
        i, o = Decimal(str(override_in)), Decimal(str(override_out))
        return Price(input=i, output=o, cache_read=i / 10, cache_write=i * Decimal("1.25"))
    return PRICES.get(model)


def cost_usd(
    price: Price | None,
    *,
    input_tokens: int,
    output_tokens: int,
    cache_read: int = 0,
    cache_write: int = 0,
) -> Decimal:
    """Cost of one call. `input_tokens` excludes cached tokens (Anthropic
    reports them separately; the OpenAI provider subtracts them)."""
    if price is None:
        return Decimal(0)
    total = (
        price.input * input_tokens
        + price.output * output_tokens
        + price.cache_read * cache_read
        + price.cache_write * cache_write
    ) / MILLION
    return total.quantize(Decimal("0.000001"))
