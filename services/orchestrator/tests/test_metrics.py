from __future__ import annotations

from decimal import Decimal

from devassist_orchestrator.llm import LLMResponse
from devassist_orchestrator.metrics import record_usage
from prometheus_client import REGISTRY


def value(name: str, labels: dict[str, str]) -> float:
    return REGISTRY.get_sample_value(name, labels) or 0.0


def test_record_usage_counts_tokens_and_cost() -> None:
    labels = {"agent": "coder", "model": "m-test"}
    before_in = value("llm_tokens_total", {**labels, "kind": "input"})
    before_cache = value("llm_tokens_total", {**labels, "kind": "cache_read"})
    before_cost = value("llm_cost_usd_total", labels)

    record_usage(
        "coder",
        LLMResponse(
            text="{}",
            data={},
            provider="mock",
            model="m-test",
            input_tokens=100,
            output_tokens=20,
            cache_read_tokens=900,
            cost_usd=Decimal("0.0125"),
            latency_ms=1500,
        ),
    )

    assert value("llm_tokens_total", {**labels, "kind": "input"}) == before_in + 100
    assert value("llm_tokens_total", {**labels, "kind": "cache_read"}) == before_cache + 900
    assert abs(value("llm_cost_usd_total", labels) - before_cost - 0.0125) < 1e-9
    assert value("agent_step_duration_seconds_count", {"agent": "coder"}) >= 1
