// Command sandbox-runner validates candidate patches: for each patch it
// starts an isolated, resource-limited container, applies the diff and runs
// the repo's tests, a security scan and static analysis.
//
// Phase 1: the process lifecycle only. The runner is stateless with respect
// to Postgres (results go out as validation.completed events), so it depends
// only on Redis (locks) and Kafka. Sandboxing arrives in Phase 3.
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
	"github.com/redis/go-redis/v9"
)

const (
	serviceName = "sandbox-runner"
	defaultPort = 8081
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("redis url: %w", err)
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()

	mux := http.NewServeMux()
	health.Register(mux, map[string]health.Check{
		"redis": health.Redis(rdb),
		"kafka": health.Kafka(cfg.KafkaBrokers, []string{
			string(events.PatchGenerated), string(events.ValidationCompleted),
		}),
	}, cfg.ReadinessTimeout)

	log.Info("starting", "port", cfg.Port, "env", cfg.Env)
	return httpserver.Run(ctx, log, ":"+strconv.Itoa(cfg.Port), mux, cfg.ShutdownGracePeriod)
}
