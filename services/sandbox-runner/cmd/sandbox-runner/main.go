// Command sandbox-runner validates candidate patches in isolated containers.
//
//	sandbox-runner [serve]     consume patch.generated + serve HTTP (default)
//	sandbox-runner validate    validate one patch from the command line
//	sandbox-runner healthcheck probe /healthz (Docker HEALTHCHECK)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Shaj2x/devassist/libs/gocommon/config"
	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/libs/gocommon/health"
	"github.com/Shaj2x/devassist/libs/gocommon/httpserver"
	"github.com/Shaj2x/devassist/libs/gocommon/kafkax"
	"github.com/Shaj2x/devassist/libs/gocommon/logging"
	rconfig "github.com/Shaj2x/devassist/services/sandbox-runner/internal/config"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/consumer"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/httpapi"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/sandbox"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/toolchain"
	"github.com/Shaj2x/devassist/services/sandbox-runner/internal/validate"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

const serviceName = "sandbox-runner"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	if cmd == "healthcheck" {
		return health.ProbeLocal(config.String("PORT", "8081"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx)
	case "validate":
		err = validateOnce(ctx, args)
	default:
		err = fmt.Errorf("unknown command %q (want serve, validate or healthcheck)", cmd)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", serviceName, err)
		return 1
	}
	return 0
}

func newValidator(ctx context.Context, cfg rconfig.Config, log *slog.Logger) (*validate.Validator, *sandbox.Docker, error) {
	docker, err := sandbox.NewDocker()
	if err != nil {
		return nil, nil, err
	}
	if err := docker.Ping(ctx); err != nil {
		_ = docker.Close()
		return nil, nil, fmt.Errorf("docker daemon unreachable: %w", err)
	}
	if cfg.EgressNetwork != "" {
		if err := docker.EnsureNetwork(ctx, cfg.EgressNetwork); err != nil {
			_ = docker.Close()
			return nil, nil, fmt.Errorf("egress network: %w", err)
		}
	}
	v := validate.New(validate.FromDocker(docker), validate.Options{
		Images: cfg.Images, Limits: cfg.Limits, EgressNetwork: cfg.EgressNetwork,
		DefaultTimeout: cfg.Timeout, StepTimeout: cfg.StepTimeout, WorkDir: cfg.WorkDir,
		GitHubToken: cfg.GitHubToken, Concurrency: cfg.Concurrency,
	}, log)
	return v, docker, nil
}

func serve(ctx context.Context) error {
	cfg, err := rconfig.Load()
	if err != nil {
		return err
	}
	log := logging.New(serviceName, cfg.LogLevel)

	validator, docker, err := newValidator(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer docker.Close()

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("redis url: %w", err)
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()

	writer := kafkax.NewWriter(cfg.KafkaBrokers)
	defer writer.Close()
	handler := consumer.NewHandler(validator, kafkax.NewPublisher(writer, serviceName), rdb,
		cfg.ResultCacheTTL, cfg.Timeout+time.Minute, log)

	mux := http.NewServeMux()
	health.Register(mux, map[string]health.Check{
		"redis":  health.Redis(rdb),
		"kafka":  health.Kafka(cfg.KafkaBrokers, []string{string(events.PatchGenerated), string(events.ValidationCompleted)}),
		"docker": docker.Ping,
	}, cfg.ReadinessTimeout)
	httpapi.Register(mux, validator)

	log.InfoContext(ctx, "starting", "port", cfg.Port, "concurrency", cfg.Concurrency,
		"egress_network", cfg.EgressNetwork, "memory", cfg.Limits.MemoryBytes, "timeout", cfg.Timeout.String())

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return httpserver.Run(ctx, log, ":"+strconv.Itoa(cfg.Port), mux, cfg.ShutdownGracePeriod)
	})
	// One consumer per concurrency slot; partitions are spread across them.
	for i := range cfg.Concurrency {
		reader := kafkax.NewReader(cfg.KafkaBrokers, cfg.KafkaGroupPrefix+"-sandbox-runner", string(events.PatchGenerated))
		cons := kafkax.NewConsumer(reader, writer, handler.Handle, log.With("consumer", i), kafkax.ConsumerConfig{})
		g.Go(func() error {
			defer reader.Close()
			return cons.Run(ctx)
		})
	}
	g.Go(func() error { reap(ctx, docker, cfg.Timeout+5*time.Minute, log); return nil })
	return g.Wait()
}

// reap removes sandboxes older than maxAge every few minutes. In normal
// operation each validation removes its own container; this catches the ones
// left behind if the runner itself crashed or was killed.
func reap(ctx context.Context, docker *sandbox.Docker, maxAge time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		if n, err := docker.Reap(ctx, maxAge); err != nil {
			log.WarnContext(ctx, "reaper failed", "error", err)
		} else if n > 0 {
			log.WarnContext(ctx, "reaped orphaned sandboxes", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// validateOnce validates one patch and prints a summary (demo/CLI).
func validateOnce(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	url := fs.String("url", "", "clone URL")
	sha := fs.String("sha", "", "commit to check out")
	diffPath := fs.String("diff", "-", "patch file (- for stdin)")
	lang := fs.String("lang", "", "override language detection")
	asJSON := fs.Bool("json", false, "print the full validation.completed payload")
	_ = fs.Parse(args)
	if *url == "" || *sha == "" {
		return errors.New("validate: -url and -sha are required")
	}
	var diff []byte
	var err error
	if *diffPath == "-" {
		diff, err = io.ReadAll(os.Stdin)
	} else {
		diff, err = os.ReadFile(*diffPath)
	}
	if err != nil {
		return err
	}

	cfg, err := rconfig.Load()
	if err != nil {
		return err
	}
	log := logging.New(serviceName, cfg.LogLevel)
	validator, docker, err := newValidator(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer docker.Close()

	res := validator.Validate(ctx, validate.Request{JobID: "cli", PatchID: "cli", Iteration: 1,
		CloneURL: *url, CommitSHA: *sha, Diff: string(diff), Overrides: toolchain.Overrides{Language: *lang}})
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	printSummary(res)
	return nil
}

func printSummary(r events.ValidationCompletedPayload) {
	fmt.Printf("status: %s  (%.1fs)\n", strings.ToUpper(r.Status), float64(r.DurationMS)/1000)
	if r.Error != nil {
		fmt.Printf("error:  %s\n", *r.Error)
	}
	for _, c := range []struct {
		name string
		res  events.CheckResult
	}{{"tests", r.Tests}, {"security", r.Security}, {"static", r.StaticAnalysis}} {
		summary := ""
		if c.res.Summary != nil {
			summary = *c.res.Summary
		}
		fmt.Printf("  %-9s %-8s %s\n", c.name, c.res.Status, summary)
		for _, f := range c.res.Findings {
			loc := ""
			if f.File != nil {
				loc = *f.File
				if f.Line != nil {
					loc += ":" + strconv.Itoa(*f.Line)
				}
			}
			rule := ""
			if f.RuleID != nil {
				rule = *f.RuleID
			}
			fmt.Printf("      - %s %s %s\n", rule, loc, firstLine(f.Message))
		}
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
