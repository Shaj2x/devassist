# Sample repositories

Small demo repos used to exercise the full pipeline (indexing, the agent
loop, sandbox validation). They are inputs to DevAssist, not part of its
codebase, so the monorepo's linters and tests skip them.

| Repo      | Language | Planted problem                                        |
| --------- | -------- | ------------------------------------------------------ |
| `datekit` | Python   | `is_leap_year` ignores century rules; one test fails   |

`make sample-repos` (run automatically by `make up`) publishes each one as a
bare git repository under `.data/sample-repos/`, mounted into the containers
as `file:///sample-repos/<name>.git`. Commits use a fixed author and date, so
the commit SHA is reproducible.
