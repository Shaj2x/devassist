"""API metrics: what humans decide and whether GitHub cooperates."""

from __future__ import annotations

from prometheus_client import Counter

JOBS_CREATED = Counter("jobs_created_total", "Jobs submitted through the API.")
REVIEW_DECISIONS = Counter(
    "review_decisions_total",
    "Human review decisions (approve, reject, request_changes).",
    ["decision"],
)
PULL_REQUESTS = Counter(
    "pull_requests_total", "Pull request attempts by outcome (opened, failed).", ["outcome"]
)
