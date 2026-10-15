"""Orchestrator metrics: what the agents cost, how long they take, how jobs end."""

from __future__ import annotations

from prometheus_client import Counter, Histogram

from devassist_orchestrator.llm import LLMResponse

AGENT_STEPS = Counter(
    "agent_steps_total",
    "Agent LLM calls by agent and outcome (succeeded, invalid_output, llm_error).",
    ["agent", "outcome"],
)
AGENT_LATENCY = Histogram(
    "agent_step_duration_seconds",
    "LLM latency per agent call.",
    ["agent"],
    buckets=(0.5, 1, 2.5, 5, 10, 20, 30, 60, 120, 180, 300),
)
LLM_TOKENS = Counter(
    "llm_tokens_total",
    "Tokens by agent and kind (input, output, cache_read, cache_write).",
    ["agent", "model", "kind"],
)
LLM_COST = Counter("llm_cost_usd_total", "Estimated model spend in USD.", ["agent", "model"])
JOBS = Counter(
    "jobs_finished_total",
    "Jobs by how the agent loop ended (awaiting_review, failed).",
    ["outcome"],
)
JOB_ITERATIONS = Histogram(
    "job_iterations", "Code/validate iterations a job needed.", buckets=(1, 2, 3, 4, 5, 7, 10)
)
JOB_DURATION = Histogram(
    "job_duration_seconds",
    "Wall time of one agent-loop run.",
    buckets=(5, 15, 30, 60, 120, 300, 600, 900, 1800, 3600),
)


def record_usage(agent: str, response: LLMResponse) -> None:
    """Latency, tokens and cost of one completed LLM call."""
    AGENT_LATENCY.labels(agent).observe(response.latency_ms / 1000)
    for kind, n in (
        ("input", response.input_tokens),
        ("output", response.output_tokens),
        ("cache_read", response.cache_read_tokens),
        ("cache_write", response.cache_write_tokens),
    ):
        if n:
            LLM_TOKENS.labels(agent, response.model, kind).inc(n)
    if response.cost_usd:
        LLM_COST.labels(agent, response.model).inc(float(response.cost_usd))
