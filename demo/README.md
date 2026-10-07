# Demo patches

Hand-written patches against `sample-repos/datekit`, used to show the sandbox
runner's verdicts without involving an LLM:

| Patch                          | Expected verdict                                                  |
| ------------------------------ | ----------------------------------------------------------------- |
| `datekit-fix-leap-year.diff`   | **passed**: fixes the century rule, all 14 tests pass             |
| `datekit-broken-fix.diff`      | **failed**: "fix" breaks ordinary leap years; tests fail          |
| `datekit-insecure-helper.diff` | **failed**: tests pass, but bandit flags `shell=True` (high) and ruff flags an undefined name |

```bash
make demo-validate patch=datekit-fix-leap-year
```
