from datetime import datetime, timedelta

from datekit.formatting import format_relative, humanize_duration


def test_humanize_duration():
    assert humanize_duration(0) == "0 seconds"
    assert humanize_duration(1) == "1 second"
    assert humanize_duration(3725) == "1 hour, 2 minutes"
    assert humanize_duration(90061, max_parts=3) == "1 day, 1 hour, 1 minute"


def test_format_relative():
    now = datetime(2024, 1, 1, 12, 0, 0)
    assert format_relative(now + timedelta(seconds=30), now) == "just now"
    assert format_relative(now + timedelta(hours=2), now) == "in 2 hours"
    assert format_relative(now - timedelta(minutes=5), now) == "5 minutes ago"
