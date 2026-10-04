package health

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
)

// Postgres pings the pool.
func Postgres(pool *pgxpool.Pool) Check {
	return func(ctx context.Context) error { return pool.Ping(ctx) }
}

// Redis pings the client.
func Redis(client *redis.Client) Check {
	return func(ctx context.Context) error { return client.Ping(ctx).Err() }
}

// Kafka dials a broker and verifies the given topics exist.
func Kafka(brokers []string, requiredTopics []string) Check {
	return func(ctx context.Context) error {
		if len(brokers) == 0 {
			return fmt.Errorf("no brokers configured")
		}
		conn, err := (&kafka.Dialer{}).DialContext(ctx, "tcp", brokers[0])
		if err != nil {
			return err
		}
		defer conn.Close()
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}
		partitions, err := conn.ReadPartitions()
		if err != nil {
			return err
		}
		have := make(map[string]bool)
		for _, p := range partitions {
			have[p.Topic] = true
		}
		var missing []string
		for _, t := range requiredTopics {
			if !have[t] {
				missing = append(missing, t)
			}
		}
		if len(missing) > 0 {
			slices.Sort(missing)
			return fmt.Errorf("missing topics: %s", strings.Join(missing, ", "))
		}
		return nil
	}
}
