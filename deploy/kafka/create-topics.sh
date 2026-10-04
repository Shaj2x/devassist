#!/bin/bash
# Creates every DevAssist topic and its dead-letter topic. Idempotent: runs on
# every `make up` via the kafka-init container and exits 0 if topics exist.
set -euo pipefail

BOOTSTRAP="${KAFKA_BOOTSTRAP:-kafka:9092}"
PARTITIONS="${KAFKA_TOPIC_PARTITIONS:-3}"
TOPICS=(repo.registered repo.indexed job.created patch.generated validation.completed job.completed)

for topic in "${TOPICS[@]}"; do
  for name in "$topic" "$topic.dlq"; do
    /opt/kafka/bin/kafka-topics.sh --bootstrap-server "$BOOTSTRAP" --create --if-not-exists \
      --topic "$name" --partitions "$PARTITIONS" --replication-factor 1 \
      --config retention.ms=604800000 >/dev/null
    echo "topic ready: $name"
  done
done
