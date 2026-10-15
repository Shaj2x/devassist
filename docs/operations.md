# Operations

How to run, watch and scale DevAssist: metrics, alerts, Kubernetes, CI, and
what to do when something goes wrong.

## Metrics

Every service serves Prometheus metrics on `GET /metrics` (api :8000,
orchestrator :8001, indexer :8080, sandbox-runner :8081). In Kubernetes the
pods carry `prometheus.io/scrape` annotations, and a NetworkPolicy lets the
`monitoring` namespace scrape them. Label values are bounded on purpose:
HTTP metrics use the route template (`/v1/jobs/{job_id}`), never the path.

| Metric | Service | Labels | What it tells you |
| --- | --- | --- | --- |
| `http_requests_total`, `http_request_duration_seconds` | all | route, method, status | Traffic, error rate and latency per endpoint |
| `kafka_messages_total` | all consumers | topic, outcome (`processed`, `retried`, `dead_lettered`) | Poison messages and flaky dependencies |
| `kafka_handler_duration_seconds` | all consumers | topic | Time per message, retries included |
| `jobs_created_total` | api | | Demand |
| `review_decisions_total` | api | decision (`approve`, `reject`, `request_changes`) | How often humans accept the agents' work |
| `pull_requests_total` | api | outcome (`opened`, `failed`) | GitHub token or permission problems |
| `jobs_finished_total` | orchestrator | outcome (`awaiting_review`, `failed`) | Agent success rate |
| `job_iterations`, `job_duration_seconds` | orchestrator | | How hard tasks are; how long users wait |
| `agent_steps_total` | orchestrator | agent, outcome (`succeeded`, `invalid_output`, `llm_error`) | Which agent struggles, provider outages |
| `agent_step_duration_seconds` | orchestrator | agent | Model latency per role |
| `llm_tokens_total` | orchestrator | agent, model, kind (`input`, `output`, `cache_read`, `cache_write`) | Usage, and whether prompt caching works |
| `llm_cost_usd_total` | orchestrator | agent, model | Spend, per role and model |
| `indexer_runs_total` | indexer | outcome (`indexed`, `skipped`, `busy`, `failed`) | Indexing health |
| `indexer_run_duration_seconds` | indexer | | Time to index a repository |
| `indexer_chunks_total` | indexer | source (`embedded`, `reused`) | How much incremental reindexing saves |
| `sandbox_validations_total`, `sandbox_validation_duration_seconds` | sandbox-runner | status | Patch pass rate, sandbox time |
| `sandbox_checks_total` | sandbox-runner | check, status | Which gate (tests, security, static) fails |
| `sandbox_active` | sandbox-runner | | Sandboxes running now |
| `sandbox_slot_wait_seconds` | sandbox-runner | | Queueing for a sandbox: sustained waits mean scale out |

Go and Python runtime metrics (goroutines, GC, memory, file descriptors)
come for free on every service.

### Suggested alerts

```yaml
groups:
  - name: devassist
    rules:
      - alert: DevAssistDeadLetters
        expr: increase(kafka_messages_total{outcome="dead_lettered"}[15m]) > 0
        annotations:
          summary: "Messages dead-lettered on {{ $labels.topic }}: inspect {{ $labels.topic }}.dlq"
      - alert: DevAssistLLMErrors
        expr: sum(rate(agent_steps_total{outcome="llm_error"}[10m])) / sum(rate(agent_steps_total[10m])) > 0.2
        for: 10m
      - alert: DevAssistSpendSpike
        expr: sum(increase(llm_cost_usd_total[1h])) > 20
      - alert: DevAssistSandboxBacklog
        expr: histogram_quantile(0.9, sum by (le) (rate(sandbox_slot_wait_seconds_bucket[15m]))) > 60
        for: 15m
      - alert: DevAssistAPIErrors
        expr: sum(rate(http_requests_total{status=~"5.."}[5m])) / sum(rate(http_requests_total[5m])) > 0.05
        for: 5m
```

## Logs

JSON lines on stdout, one object per event, with `service`, `level`,
`trace_id` and structured fields. The trace id is the job id (or the
repository id for indexing) and travels in every Kafka envelope, so a job's
whole story across the api, orchestrator, indexer and sandbox runner is one
filter on `trace_id`. Prompts, tokens and secrets are never logged; prompts
and responses live in `agent_steps` instead.

## Kubernetes

```
deploy/k8s/
  base/                apps: Deployments, Services, HPA, PDBs, NetworkPolicies, Ingress, migrate Job
  components/infra/    in-cluster Postgres (pgvector), Redis, single-node KRaft Kafka + topic Job
  overlays/dev/        base + infra, mock model, sample repos, local images (kind)
  overlays/prod/       base only, managed infra, GHCR images, token auth, KEDA, sandbox node pool
```

**Local cluster.** `make k8s-dev` builds the images, creates a kind cluster
and a local registry, loads the images, applies `overlays/dev` and waits for
every rollout. Then:

```bash
kubectl -n devassist port-forward svc/dashboard 3000:80   # http://localhost:3000
kubectl -n devassist port-forward svc/api 8000:8000       # make demo-api works unchanged
make k8s-down
```

**Production.** Provision Postgres 16 with the `vector` extension, Redis and
Kafka (create the topics and their `.dlq` twins, as
`deploy/kafka/create-topics.sh` does), install ingress-nginx, cert-manager
and KEDA, label and taint the sandbox nodes (see
`overlays/prod/sandbox-pool.yaml`), create the `devassist-secrets` Secret
through your secret manager, set the image tag, and apply:

```bash
cd deploy/k8s/overlays/prod
kustomize edit set image devassist/api=ghcr.io/shaj2x/devassist-api:<sha> ...
kubectl apply -k .
```

The `migrate` Job runs on every apply and is a no-op when the schema is
current. Migrations are additive, so the old version keeps working while it
runs.

**Scaling.** The api scales on CPU (HPA). The orchestrator, sandbox runner
and indexer scale on Kafka consumer lag (KEDA ScaledObjects in the prod
overlay), which is the real measure of their backlog; replicas are capped at
the partition count (3), beyond which extra consumers would sit idle.
Sandbox throughput per runner pod is `SANDBOX_CONCURRENCY`; the pod's
resource requests must cover that many sandboxes.

**Sandbox isolation in a cluster.** Each runner pod has its own Docker
daemon in a privileged sidecar, so sandboxes live and die with the pod, never
share a daemon with other workloads, and the node's container socket is never
mounted. The runner itself is unprivileged, non-root and read-only; the
untrusted code runs one level further down in the sandboxes. In production,
the runner pods go to a dedicated, tainted node pool, and Kata Containers can
add a VM boundary around the whole pod.

## CI

[`.github/workflows/ci.yml`](../.github/workflows/ci.yml) runs on every push
and pull request:

| Job | Runs |
| --- | --- |
| Python | ruff, ruff format, mypy --strict per package, pytest |
| Go (per module) | gofmt, go vet, golangci-lint, go test -race |
| Dashboard | eslint, tsc, vitest, production build |
| Kubernetes manifests | render every overlay, validate with kubeconform (CRDs included) |
| Integration | `make up`, then migrations, indexer, sandbox escape attempts, agent pipeline and API end-to-end tests, then `make smoke` |
| Publish images | on `main` only, after everything passes: every service and sandbox image to GHCR, tagged with the commit and `latest` |

Dependabot keeps actions and Python, Go and npm dependencies current.

## Runbook

**A job is stuck in a running state.** Check `devassist:job:<id>:progress`
in Redis and the orchestrator logs for its trace id. If the worker died, the
job's `job.created` message is redelivered and the job re-runs once the
per-job lock expires (2 minutes). `job.completed` repairs a status left
running.

**Messages in a `.dlq` topic.** The headers say why (`x-error`), after how
many attempts (`x-attempts`) and from which offset. Fix the cause, then
replay with any Kafka tool (copy the message back to the original topic):
every handler is idempotent.

**Validations time out.** Check `sandbox_slot_wait_seconds` (queueing: scale
out the runner) and `sandbox_validation_duration_seconds` (slow tests: raise
`SANDBOX_TIMEOUT` or set a repo's `timeout_seconds`).

**Spend is higher than expected.** `llm_cost_usd_total` by agent and model
shows where it goes, and `llm_tokens_total{kind="cache_read"}` whether prompt
caching is working. `JOB_BUDGET_USD` caps every job.
