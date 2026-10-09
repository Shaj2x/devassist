from __future__ import annotations

import uuid
from datetime import UTC, datetime
from decimal import Decimal

from devassist_api.pr import pull_request_body, pull_request_title
from devassist_common.db import Job, Patch, ValidationRun


def test_title_uses_first_line_and_truncates() -> None:
    assert pull_request_title("Fix leap years\n\nmore detail") == "Fix leap years"
    long = pull_request_title("x" * 200)
    assert len(long) == 72 and long.endswith("…")


def test_body_includes_plan_review_and_validation() -> None:
    job = Job(
        id=uuid.uuid4(),
        task="Fix the failing test",
        plan={"summary": "Correct the century rule", "steps": ["edit calendar.py"]},
        review={"summary": "Fixes leap years", "risk_level": "low", "concerns": ["check 1900"]},
        total_cost_usd=Decimal("0.0123"),
    )
    patch = Patch(iteration=2, files_changed=1, additions=3, deletions=1)
    patch.validation_runs = [
        ValidationRun(
            tests={"status": "passed", "summary": "22 passed"},
            security={"status": "passed"},
            static_analysis={"status": "skipped"},
            created_at=datetime.now(UTC),
        )
    ]
    body = pull_request_body(job, patch)
    assert "Correct the century rule" in body
    assert "- edit calendar.py" in body
    assert "**Risk:** low" in body and "- check 1900" in body
    assert "| Tests | passed | 22 passed |" in body
    assert "2 iteration(s)" in body and "$0.0123" in body
