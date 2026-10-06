"""Business-day arithmetic (Monday to Friday, minus holidays)."""

from collections.abc import Iterable
from datetime import date, timedelta


def is_business_day(day: date, holidays: Iterable[date] = ()) -> bool:
    """True for Monday-Friday dates that are not holidays."""
    return day.weekday() < 5 and day not in set(holidays)


def add_business_days(start: date, days: int, holidays: Iterable[date] = ()) -> date:
    """Move ``days`` business days from ``start`` (negative moves backwards)."""
    holiday_set = set(holidays)
    step = 1 if days >= 0 else -1
    current = start
    remaining = abs(days)
    while remaining:
        current += timedelta(days=step)
        if is_business_day(current, holiday_set):
            remaining -= 1
    return current
