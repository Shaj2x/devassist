// Command indexer clones repositories, chunks and embeds their source, stores
// the chunks in Postgres/pgvector and serves semantic search over them.
//
// Phase 1: the process lifecycle only (config, logging, dependency clients,
// health endpoints, graceful shutdown). Indexing arrives in Phase 2.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/Shaj2x/devassist/libs/gocommon/config"
	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/libs/gocommon/health"
	"github.com/Shaj2x/devassist/libs/gocommon/httpserver"
	"github.com/Shaj2x/devassist/libs/gocommon/logging"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	serviceName = "indexer"
	defaultPort = 8080
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		// Used by the Docker HEALTHCHECK: distroless images have no curl.
		os.Exit(health.ProbeLocal(config.String("PORT", strconv.Itoa(defaultPort))))
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", serviceName, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadBase(defaultPort)
	if err != nil {
		return err
	}
	log := logging.New(serviceName, cfg.LogLevel)

	// Cancelled on SIGINT/SIGTERM; everything below shuts down from this.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("redis url: %w", err)
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()

	mux := http.NewServeMux()
	health.Register(mux, map[string]health.Check{
		"postgres": health.Postgres(pool),
		"redis":    health.Redis(rdb),
		"kafka":    health.Kafka(cfg.KafkaBrokers, events.TopicNames()),
	}, cfg.ReadinessTimeout)

	log.Info("starting", "port", cfg.Port, "env", cfg.Env)
	return httpserver.Run(ctx, log, ":"+strconv.Itoa(cfg.Port), mux, cfg.ShutdownGracePeriod)
}
