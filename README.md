# DevAssist

DevAssist is a multi-agent AI platform that takes a plain-English change request for a GitHub repository, plans it, writes the code and tests, and validates every patch in a locked-down Docker sandbox. Validated patches land in a review dashboard where a human reads the diff, the agents' reasoning, and the test/security/lint results, and approves it straight into a pull request.

> **Status:** Phases 1-3 of 7 complete (foundation, indexer, sandbox runner). See [Roadmap](#roadmap).

## Architecture

Python (FastAPI) for the API and agent orchestration, Go for the
performance-sensitive indexer and sandbox runner, Postgres + pgvector for data
and embeddings, Redis for caching and locks, and Kafka as the event bus.
Details: [docs/architecture.md](docs/architecture.md).

```mermaid
flowchart LR
    UI[Dashboard] --> API[api · FastAPI]
    API <--> K[(Kafka)]
    K <--> IDX[indexer · Go]
    K <--> ORC[orchestrator · agents]
    K <--> SBX[sandbox-runner · Go]
    ORC --> IDX
    API & ORC & IDX --> PG[(Postgres + pgvector)]
    API & ORC & IDX & SBX --> R[(Redis)]
```

## Quickstart

Requirements: Docker with Compose v2, and `make`.

```bash
git clone https://github.com/Shaj2x/devassist.git
cd devassist
make up      # builds every image, starts the stack, waits until all services are healthy
make smoke   # hits every service's /readyz
```

| URL                              | What                           |
| -------------------------------- | ------------------------------ |
| http://localhost:3000            | Dashboard                      |
| http://localhost:8000/docs       | API (OpenAPI / Swagger UI)     |
| http://localhost:8001/readyz     | Orchestrator readiness         |
| http://localhost:8080/readyz     | Indexer readiness              |
| http://localhost:8081/readyz     | Sandbox runner readiness       |

`make up` creates `.env` from [`.env.example`](.env.example) on first run and
builds the sandbox images (the first build takes a few minutes; the Go image
is the largest). No
API keys are needed for the foundation; the LLM and embedding providers
default to offline mocks.

### Try the indexer

`make up` also publishes the demo repos in `sample-repos/` as local git
remotes. Index one and search it:

```bash
make demo-index                                  # clone, chunk, embed, store
make demo-search q="parse a duration like 1h30m"
```

```
1. datekit/parsing.py:20-27  [function parse_duration]  score=0.331
     def parse_duration(text: str) -> timedelta:
         """Parse compact durations like ``1h30m``, ``2d``, or ``45s``."""
```

How chunking, incremental reindexing and caching work:
[docs/indexer.md](docs/indexer.md).

### Try the sandbox

Validate hand-written patches against the same sample repo. Each runs in a
fresh locked-down container (no network, read-only root, non-root, resource
and time limits) that is removed afterwards:

```bash
make demo-validate patch=datekit-fix-leap-year     # PASSED: 14 tests
make demo-validate patch=datekit-broken-fix        # FAILED: 3 failing tests
make demo-validate patch=datekit-insecure-helper   # FAILED: bandit B602 + ruff F821
```

```
status: FAILED  (1.1s)
  tests     passed   14 passed, 0 failed, 0 skipped
  security  failed   bandit: 2 finding(s), 1 blocking
      - B602 datekit/calendar.py:15 subprocess call with shell=True identified, security issue.
  static    failed   ruff: 1 finding(s), 1 blocking
      - F821 datekit/calendar.py:19 Undefined name `is_valid`
```

How isolation works, and the tests that try to break it:
[docs/sandbox.md](docs/sandbox.md).

Building behind a TLS-inspecting corporate proxy? See
[deploy/certs/README.md](deploy/certs/README.md).

## Development

For local tooling you need [uv](https://docs.astral.sh/uv/), Go 1.24+, Node 22+
and golangci-lint v2.

```bash
make install           # uv sync, go mod download, npm ci
make test              # unit tests: Python, Go, dashboard
make lint              # ruff + mypy --strict, gofmt + go vet + golangci-lint, eslint + tsc
make test-integration  # migrations, indexing pipeline, sandbox escape attempts (needs `make up`)
make logs s=api        # follow one service's JSON logs
make down              # stop (keeps data);  make clean  # stop and wipe volumes
```

## Repository layout

```
libs/devassist-common/  shared Python: config, JSON logging, events, DB models, health checks
libs/gocommon/          shared Go: the same, for the Go services
services/api/           FastAPI service + Alembic migrations (owns the schema)
services/orchestrator/  multi-agent workflow (Phase 4)
services/indexer/       Go: clone, chunk, embed, semantic search
services/sandbox-runner/ Go: isolated patch validation
dashboard/              React + TypeScript + Vite review UI
proto/events/           JSON Schemas for every Kafka event + shared fixtures
sample-repos/           small demo repositories DevAssist works on
demo/patches/           hand-written patches for the sandbox demo
deploy/                 docker-compose, Kafka topic setup, k8s (Phase 7)
docs/                   architecture and design notes
```

## Roadmap

- [x] **Phase 1: Foundation.** Monorepo, docker-compose (Postgres/pgvector,
  Redis, Kafka in KRaft mode), service skeletons with health checks, full
  schema migration, event contracts, Makefile.
- [x] **Phase 2: Indexer.** Clone, tree-sitter chunking, pluggable embeddings
  with incremental reuse, pgvector search, symbol lookup, Kafka consumer with
  retries and dead-lettering, Redis locks and caching.
- [x] **Phase 3: Sandbox runner.** Hardened disposable containers running
  tests, security scans and static analysis for Python, Go and JS/TS, with
  escape-attempt integration tests and guaranteed teardown.
- [ ] **Phase 4: Agents.** Planner, Coder, Tester, Debugger, Reviewer state machine.
- [ ] **Phase 5: API + GitHub.** REST endpoints, Kafka wiring, PR creation.
- [ ] **Phase 6: Dashboard.** Live job timeline, diff viewer, validation results, approve flow.
- [ ] **Phase 7: Deployment + polish.** Kubernetes, CI, metrics, full docs.
