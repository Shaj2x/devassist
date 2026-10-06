from datetime import date

from datekit.business import add_business_days, is_business_day


def test_is_business_day():
    assert is_business_day(date(2024, 1, 5))  # Friday
    assert not is_business_day(date(2024, 1, 6))  # Saturday
    assert not is_business_day(date(2024, 1, 1), holidays=[date(2024, 1, 1)])


def test_add_business_days_skips_weekends_and_holidays():
    friday = date(2024, 1, 5)
    assert add_business_days(friday, 1) == date(2024, 1, 8)
    assert add_business_days(friday, 1, holidays=[date(2024, 1, 8)]) == date(2024, 1, 9)
    assert add_business_days(date(2024, 1, 8), -1) == friday
