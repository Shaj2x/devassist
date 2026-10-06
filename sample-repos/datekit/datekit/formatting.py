"""Human-friendly formatting of durations and relative times."""

from datetime import datetime

_UNITS = (("day", 86400), ("hour", 3600), ("minute", 60), ("second", 1))


def humanize_duration(seconds: int, max_parts: int = 2) -> str:
    """Format a duration: ``3725`` -> ``"1 hour, 2 minutes"``."""
    if seconds < 0:
        raise ValueError("duration must be non-negative")
    if seconds == 0:
        return "0 seconds"
    parts = []
    for name, size in _UNITS:
        amount, seconds = divmod(seconds, size)
        if amount:
            parts.append(f"{amount} {name}{'' if amount == 1 else 's'}")
    return ", ".join(parts[:max_parts])


def format_relative(moment: datetime, now: datetime) -> str:
    """Describe ``moment`` relative to ``now``: ``"in 2 hours"`` or ``"5 minutes ago"``."""
    delta = int((moment - now).total_seconds())
    if abs(delta) < 60:
        return "just now"
    text = humanize_duration(abs(delta), max_parts=1)
    return f"in {text}" if delta > 0 else f"{text} ago"
