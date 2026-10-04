"""Contract tests: the Python event models agree with proto/events schemas."""

from __future__ import annotations

import json
import uuid
from pathlib import Path
from typing import Any

import jsonschema
import pytest
from devassist_common.events import (
    PAYLOAD_MODELS,
    TOPICS,
    Envelope,
    EventType,
    JobCreated,
    dlq_topic,
    new_event,
)

PROTO = Path(__file__).resolve().parents[3] / "proto" / "events"
FORMAT_CHECKER = jsonschema.Draft202012Validator.FORMAT_CHECKER


def load(path: Path) -> dict[str, Any]:
    data: dict[str, Any] = json.loads(path.read_text())
    return data


def validate(instance: dict[str, Any], schema: dict[str, Any]) -> None:
    jsonschema.Draft202012Validator(schema, format_checker=FORMAT_CHECKER).validate(instance)


@pytest.mark.parametrize("event_type", list(EventType), ids=str)
def test_example_matches_schema_and_model(event_type: EventType) -> None:
    example = load(PROTO / "examples" / f"{event_type}.json")

    validate(example, load(PROTO / "envelope.schema.json"))
    validate(example["payload"], load(PROTO / "payloads" / f"{event_type}.schema.json"))

    envelope = Envelope.model_validate(example)
    assert envelope.type is event_type
    assert isinstance(envelope.typed_payload(), PAYLOAD_MODELS[event_type])


def test_schema_enum_lists_every_event_type() -> None:
    schema_types = load(PROTO / "envelope.schema.json")["properties"]["type"]["enum"]
    assert sorted(schema_types) == sorted(TOPICS)


@pytest.mark.parametrize("event_type", list(EventType), ids=str)
def test_model_output_validates_against_schema(event_type: EventType) -> None:
    """What we produce must satisfy the schema, not just what we consume."""
    example = load(PROTO / "examples" / f"{event_type}.json")
    model = PAYLOAD_MODELS[event_type].model_validate(example["payload"])
    produced = new_event(event_type, model, source="test", trace_id="t-1")

    as_json = json.loads(produced.to_bytes())
    validate(as_json, load(PROTO / "envelope.schema.json"))
    validate(as_json["payload"], load(PROTO / "payloads" / f"{event_type}.schema.json"))


def test_round_trip_bytes() -> None:
    payload = JobCreated(job_id=uuid.uuid4(), repo_id=uuid.uuid4(), task="x", max_iterations=3)
    event = new_event(EventType.JOB_CREATED, payload, source="api", trace_id=str(payload.job_id))

    decoded = Envelope.from_bytes(event.to_bytes())

    assert decoded == event
    assert decoded.typed_payload() == payload


def test_envelope_rejects_unknown_fields() -> None:
    example = load(PROTO / "examples" / "job.created.json")
    example["surprise"] = True
    with pytest.raises(ValueError, match="surprise"):
        Envelope.model_validate(example)


def test_payload_validation_errors_surface() -> None:
    example = load(PROTO / "examples" / "job.created.json")
    example["payload"]["max_iterations"] = 0
    with pytest.raises(ValueError, match="max_iterations"):
        Envelope.model_validate(example).typed_payload()


def test_dlq_topic_name() -> None:
    assert dlq_topic("job.created") == "job.created.dlq"
