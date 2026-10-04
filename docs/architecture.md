# Architecture

```mermaid
flowchart LR
    UI[Dashboard<br/>React + TS] -->|REST /api| API[api<br/>FastAPI]
    API -->|repo.registered| K[(Kafka)]
    API -->|job.created| K
    K -->|repo.registered| IDX[indexer<br/>Go]
    K -->|job.created| ORC[orchestrator<br/>Python agents]
    ORC -->|semantic search| IDX
    ORC -->|patch.generated| K
    K -->|patch.generated| SBX[sandbox-runner<br/>Go]
    SBX -->|ephemeral containers| D[[Docker]]
    SBX -->|validation.completed| K
    K -->|validation.completed| ORC
    ORC -->|job.completed| K
    K --> API
    API --> PG[(Postgres<br/>+ pgvector)]
    ORC --> PG
    IDX --> PG
    API --> R[(Redis)]
    ORC --> R
    IDX --> R
    SBX --> R
    API -->|open PR| GH[GitHub]
```

## Services

| Service          | Language | Owns                                                   | Talks to                     |
| ---------------- | -------- | ------------------------------------------------------ | ---------------------------- |
| `api`            | Python   | users, repositories, job creation, review, PRs         | Postgres, Redis, Kafka, GitHub |
| `orchestrator`   | Python   | agent loop, agent_steps, patches, validation_runs      | Postgres, Redis, Kafka, indexer, LLM |
| `indexer`        | Go       | repo_snapshots, code_chunks (embeddings), search       | Postgres, Redis, Kafka, LLM embeddings |
| `sandbox-runner` | Go       | nothing persistent: results travel as events           | Redis, Kafka, Docker         |
| `dashboard`      | TS       | UI only                                                | api (via `/api` proxy)       |

## Cross-cutting conventions

- **Config:** environment variables only; see `.env.example`.
- **Health:** every service serves `/healthz` (process alive) and `/readyz`
  (dependencies reachable, Kafka topics present).
- **Logs:** one JSON object per line with `time`, `level`, `service`, `msg`
  and, inside a job, `trace_id`. Filter on `trace_id` to follow one job across
  every service.
- **Events:** one envelope for every topic, defined in
  [`proto/events`](../proto/events/README.md). Both languages test against the
  same fixtures.
- **Schema:** owned by Alembic in `services/api/alembic`. The Go indexer
  writes to `repo_snapshots` and `code_chunks` but never migrates.

## Data model

```mermaid
erDiagram
    users ||--o{ repositories : owns
    repositories ||--o{ repo_snapshots : "indexed at"
    repo_snapshots ||--o{ code_chunks : contains
    repositories ||--o{ jobs : "changes requested on"
    jobs ||--o{ agent_steps : "trace"
    jobs ||--o{ patches : "one per iteration"
    patches ||--o{ validation_runs : "validated by"
    jobs ||--o| pull_requests : "approved into"
```

Idempotency is enforced by the schema, not by hope:
`repo_snapshots(repo_id, commit_sha)`, `patches(job_id, iteration)`,
`agent_steps(job_id, sequence)`, `validation_runs(source_event_id)`, and the
`processed_events(consumer, event_id)` ledger are all unique.
