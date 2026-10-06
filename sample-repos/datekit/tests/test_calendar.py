import pytest

from datekit.calendar import day_of_year, days_in_month, is_leap_year


def test_common_leap_years():
    assert is_leap_year(2024)
    assert not is_leap_year(2023)


def test_century_years():
    # Divisible by 100 is not a leap year, unless also divisible by 400.
    assert not is_leap_year(1900)
    assert is_leap_year(2000)


def test_days_in_month():
    assert days_in_month(2024, 2) == 29
    assert days_in_month(2023, 2) == 28
    assert days_in_month(2023, 12) == 31
    with pytest.raises(ValueError):
        days_in_month(2023, 13)


def test_day_of_year():
    assert day_of_year(2023, 1, 1) == 1
    assert day_of_year(2024, 12, 31) == 366
    with pytest.raises(ValueError):
        day_of_year(2023, 2, 29)
