from __future__ import annotations

import uuid
from typing import Any

import pytest
from devassist_common.events import CheckResult, EventType, ValidationCompleted
from devassist_orchestrator.tools import sandbox
from devassist_orchestrator.tools.sandbox import KafkaValidator, ValidationRequest, result_key


class FakePublisher:
    def __init__(self) -> None:
        self.published: list[tuple[EventType, Any]] = []

    async def publish(self, event_type: EventType, payload: Any, *, trace_id: str) -> None:
        self.published.append((event_type, payload))


class SlowRedis:
    """BLPOP that comes back empty a few times before the result arrives."""

    def __init__(self, empty_polls: int, result: bytes | None) -> None:
        self.empty_polls, self.result = empty_polls, result
        self.timeouts: list[float] = []

    async def blpop(self, keys: list[str], timeout: float) -> tuple[bytes, bytes] | None:  # noqa: ASYNC109
        self.timeouts.append(timeout)
        if self.empty_polls > 0 or self.result is None:
            self.empty_polls -= 1
            return None
        return (keys[0].encode(), self.result)


def request() -> ValidationRequest:
    return ValidationRequest(
        job_id=str(uuid.uuid4()),
        patch_id=str(uuid.uuid4()),
        iteration=1,
        repo_id=str(uuid.uuid4()),
        clone_url="file:///repo.git",
        commit_sha="abc1234",
        diff="--- a\n+++ b\n",
        config={"language": "python", "unrelated": "dropped"},
    )


def completed(req: ValidationRequest) -> ValidationCompleted:
    ok = CheckResult(status="passed")
    return ValidationCompleted(
        job_id=uuid.UUID(req.job_id),
        patch_id=uuid.UUID(req.patch_id),
        iteration=1,
        status="passed",
        tests=ok,
        security=ok,
        static_analysis=ok,
    )


async def test_waits_across_short_blpop_slices() -> None:
    req = request()
    redis = SlowRedis(empty_polls=3, result=completed(req).model_dump_json().encode())
    publisher = FakePublisher()
    validator = KafkaValidator(publisher, redis, timeout=60)  # type: ignore[arg-type]

    result = await validator.validate(req)

    assert result.status == "passed"
    assert len(redis.timeouts) == 4
    # Every slice stays below the Redis client's 5 s socket timeout.
    assert max(redis.timeouts) <= sandbox.BLOCK_SLICE_SECONDS < 5
    ((event_type, payload),) = publisher.published
    assert event_type == EventType.PATCH_GENERATED
    assert payload.config.language == "python"
    assert str(payload.patch_id) == req.patch_id
    assert result_key(req.patch_id).endswith(req.patch_id)


async def test_gives_up_after_the_overall_timeout(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(sandbox, "BLOCK_SLICE_SECONDS", 0.01)
    validator = KafkaValidator(FakePublisher(), SlowRedis(0, None), timeout=0.05)  # type: ignore[arg-type]
    with pytest.raises(TimeoutError, match="no validation result"):
        await validator.validate(request())
