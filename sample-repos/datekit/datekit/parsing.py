"""Parsing dates and durations from strings."""

import re
from datetime import date, timedelta

_ISO_DATE = re.compile(r"^(\d{4})-(\d{2})-(\d{2})$")
_DURATION_PART = re.compile(r"(\d+)\s*([dhms])")
_UNIT_SECONDS = {"d": 86400, "h": 3600, "m": 60, "s": 1}


def parse_iso_date(text: str) -> date:
    """Parse ``YYYY-MM-DD`` into a date. Raises ValueError on bad input."""
    match = _ISO_DATE.match(text.strip())
    if not match:
        raise ValueError(f"not an ISO date: {text!r}")
    year, month, day = (int(part) for part in match.groups())
    return date(year, month, day)


def parse_duration(text: str) -> timedelta:
    """Parse compact durations like ``1h30m``, ``2d``, or ``45s``."""
    cleaned = text.strip().lower()
    parts = _DURATION_PART.findall(cleaned)
    if not parts or _DURATION_PART.sub("", cleaned).strip():
        raise ValueError(f"not a duration: {text!r}")
    seconds = sum(int(amount) * _UNIT_SECONDS[unit] for amount, unit in parts)
    return timedelta(seconds=seconds)
