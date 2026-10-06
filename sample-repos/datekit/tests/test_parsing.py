from datetime import date, timedelta

import pytest

from datekit.parsing import parse_duration, parse_iso_date


def test_parse_iso_date():
    assert parse_iso_date("2024-02-29") == date(2024, 2, 29)
    assert parse_iso_date(" 2023-01-05 ") == date(2023, 1, 5)


@pytest.mark.parametrize("bad", ["2024/02/01", "24-02-01", "2023-02-30", ""])
def test_parse_iso_date_rejects_bad_input(bad):
    with pytest.raises(ValueError):
        parse_iso_date(bad)


def test_parse_duration():
    assert parse_duration("1h30m") == timedelta(hours=1, minutes=30)
    assert parse_duration("2d") == timedelta(days=2)
    assert parse_duration("45 s") == timedelta(seconds=45)
    with pytest.raises(ValueError):
        parse_duration("soon")
