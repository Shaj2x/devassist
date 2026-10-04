from __future__ import annotations

import json
import logging

import pytest
from devassist_common.logging import configure_logging, get_trace_id, trace_context


def test_json_lines_carry_service_trace_id_and_extras(capsys: pytest.CaptureFixture[str]) -> None:
    configure_logging("test-svc", "DEBUG")
    log = logging.getLogger("devassist.test")

    with trace_context("job-123"):
        log.info("planning started", extra={"iteration": 2})
    log.warning("outside")

    lines = [json.loads(line) for line in capsys.readouterr().out.splitlines()]
    first, second = lines[-2], lines[-1]
    assert first["service"] == "test-svc"
    assert first["msg"] == "planning started"
    assert first["trace_id"] == "job-123"
    assert first["iteration"] == 2
    assert first["level"] == "INFO"
    assert "trace_id" not in second


def test_ansi_duplicate_from_uvicorn_is_dropped(capsys: pytest.CaptureFixture[str]) -> None:
    configure_logging("test-svc")
    logging.getLogger("uvicorn.error").info("ready", extra={"color_message": "\x1b[36mready"})
    entry = json.loads(capsys.readouterr().out.splitlines()[-1])
    assert "color_message" not in entry


def test_trace_context_restores_previous_value() -> None:
    with trace_context("outer"):
        with trace_context("inner"):
            assert get_trace_id() == "inner"
        assert get_trace_id() == "outer"
    assert get_trace_id() is None


def test_exceptions_are_serialised(capsys: pytest.CaptureFixture[str]) -> None:
    configure_logging("test-svc")
    try:
        raise ValueError("boom")
    except ValueError:
        logging.getLogger("x").exception("failed")
    entry = json.loads(capsys.readouterr().out.splitlines()[-1])
    assert "ValueError: boom" in entry["exc"]
