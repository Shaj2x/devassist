# Kubernetes manifests

Kustomize base plus overlays:

- `base/` - the five services with probes, resource limits, non-root and
  read-only containers, HPA, PodDisruptionBudgets, default-deny
  NetworkPolicies, Ingress and the migration Job.
- `components/infra/` - in-cluster Postgres (pgvector), Redis and Kafka.
- `overlays/dev/` - everything in one local cluster: `make k8s-dev`.
- `overlays/prod/` - managed infrastructure, GHCR images, GitHub sign-in,
  KEDA autoscaling on Kafka lag, dedicated sandbox node pool.

`make k8s-validate` renders every overlay and validates it against the
Kubernetes schemas. Deploying, scaling and sandbox isolation are covered in
[docs/operations.md](../../docs/operations.md#kubernetes).
