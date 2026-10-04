"""Structured JSON logging with a trace id carried through every line.

Usage:
    configure_logging("api", "INFO")
    log = logging.getLogger(__name__)
    with trace_context(job_id):
        log.info("planning started", extra={"iteration": 1})

Every record emitted inside `trace_context` carries `trace_id`, so a single
job can be followed across api, orchestrator, indexer and sandbox-runner logs
(the Go services emit the same field name).
"""

from __future__ import annotations

import json
import logging
import sys
from collections.abc import Iterator
from contextlib import contextmanager
from contextvars import ContextVar
from datetime import UTC, datetime
from typing import Any

_trace_id: ContextVar[str | None] = ContextVar("trace_id", default=None)

# Attributes every LogRecord has; anything else came from `extra=` and is
# copied into the JSON output.
_RESERVED = set(vars(logging.makeLogRecord({}))) | {"message", "asctime", "taskName"}


def get_trace_id() -> str | None:
    return _trace_id.get()


@contextmanager
def trace_context(trace_id: str | None) -> Iterator[None]:
    token = _trace_id.set(trace_id)
    try:
        yield
    finally:
        _trace_id.reset(token)


class JsonFormatter(logging.Formatter):
    def __init__(self, service: str) -> None:
        super().__init__()
        self.service = service

    def format(self, record: logging.LogRecord) -> str:
        entry: dict[str, Any] = {
            "time": datetime.fromtimestamp(record.created, tz=UTC).isoformat(),
            "level": record.levelname,
            "service": self.service,
            "logger": record.name,
            "msg": record.getMessage(),
        }
        trace_id = get_trace_id()
        if trace_id:
            entry["trace_id"] = trace_id
        for key, value in vars(record).items():
            if key not in _RESERVED and not key.startswith("_"):
                entry[key] = value
        if record.exc_info:
            entry["exc"] = self.formatException(record.exc_info)
        return json.dumps(entry, default=str)


def configure_logging(service: str, level: str = "INFO") -> None:
    """Route all logging (including uvicorn's) through one JSON handler on stdout."""
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JsonFormatter(service))
    root = logging.getLogger()
    root.handlers = [handler]
    root.setLevel(level.upper())
    for name in ("uvicorn", "uvicorn.error", "uvicorn.access"):
        lib_logger = logging.getLogger(name)
        lib_logger.handlers = []
        lib_logger.propagate = True
    # Kafka client internals are chatty at INFO.
    logging.getLogger("aiokafka").setLevel(logging.WARNING)
