# Event contracts

Every Kafka message in DevAssist is a JSON document shaped by
[`envelope.schema.json`](envelope.schema.json). The topic name equals the
event `type`, and the payload shape lives in `payloads/<type>.schema.json`.

| Topic / type           | Producer        | Consumers                 |
| ---------------------- | --------------- | ------------------------- |
| `repo.registered`      | api             | indexer                   |
| `repo.indexed`         | indexer         | api                       |
| `job.created`          | api             | orchestrator              |
| `patch.generated`      | orchestrator    | sandbox-runner, api       |
| `validation.completed` | sandbox-runner  | orchestrator, api         |
| `job.completed`        | orchestrator    | api                       |

Rules every producer and consumer follows:

- **Keys.** Messages are keyed by `trace_id` (job id or repo id), so all events
  for one job land on the same partition and are seen in order.
- **Idempotency.** Consumers record `(consumer, event_id)` in the
  `processed_events` table in the same transaction as their side effects and
  skip events they have already processed. Kafka delivers at least once; this
  makes processing effectively once.
- **Retries.** A failing handler is retried with backoff a bounded number of
  times. After that the message is published to `<topic>.dlq` with the error
  attached, and the consumer moves on rather than blocking the partition.
- **Versioning.** Additive payload changes keep `version`; breaking changes
  bump it and consumers handle both until producers migrate.

`examples/` holds one valid event per type. Both the Python and the Go test
suites parse these fixtures, which keeps the two implementations in sync with
the schema.
