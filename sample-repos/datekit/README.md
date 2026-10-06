# datekit

A tiny date-utilities package used as a DevAssist demo target.

It has one **known bug**: `is_leap_year` ignores the century rules, so
`tests/test_calendar.py::test_century_years` fails. That makes it a good first
task: *"Fix the failing test in datekit/calendar.py"*.

```bash
pip install pytest
pytest
```
