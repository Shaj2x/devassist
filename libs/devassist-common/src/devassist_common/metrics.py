"""Prometheus metrics for the Python services (mirrors libs/gocommon/metrics).

Collectors live on prometheus_client's default registry, which also carries
process and Python runtime metrics. HTTP metrics are labelled by the route
template ("/v1/jobs/{job_id}"), never the raw path, so ids cannot blow up
label cardinality.
"""

from __future__ import annotations

import time
from collections.abc import Awaitable, Callable

from fastapi import FastAPI, Request, Response
from prometheus_client import CONTENT_TYPE_LATEST, Counter, Histogram, generate_latest

HTTP_REQUESTS = Counter(
    "http_requests_total",
    "HTTP requests by route template, method and status code.",
    ["route", "method", "status"],
)
HTTP_DURATION = Histogram(
    "http_request_duration_seconds",
    "HTTP request latency by route template.",
    ["route", "method"],
    buckets=(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60),
)
KAFKA_MESSAGES = Counter(
    "kafka_messages_total",
    "Consumed Kafka messages by topic and outcome (processed, retried, dead_lettered).",
    ["topic", "outcome"],
)
KAFKA_HANDLER = Histogram(
    "kafka_handler_duration_seconds",
    "Time to handle one message, retries included.",
    ["topic"],
    buckets=(0.01, 0.05, 0.1, 0.5, 1, 5, 15, 30, 60, 120, 300, 900, 1800),
)


def _route(request: Request) -> str:
    route = request.scope.get("route")
    path = getattr(route, "path", None)
    return path if isinstance(path, str) else "unmatched"


def instrument(app: FastAPI) -> None:
    """Count and time every request, and serve GET /metrics."""

    @app.middleware("http")
    async def record(
        request: Request, call_next: Callable[[Request], Awaitable[Response]]
    ) -> Response:
        start = time.perf_counter()
        status = 500
        try:
            response = await call_next(request)
            status = response.status_code
            return response
        finally:
            route = _route(request)
            if route != "/metrics":  # scrapes are not traffic
                HTTP_REQUESTS.labels(route, request.method, str(status)).inc()
                HTTP_DURATION.labels(route, request.method).observe(time.perf_counter() - start)

    @app.get("/metrics", include_in_schema=False)
    async def metrics() -> Response:
        return Response(generate_latest(), media_type=CONTENT_TYPE_LATEST)
