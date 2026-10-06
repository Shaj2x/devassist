// Command indexer clones repositories, chunks and embeds their source,
// stores the chunks in Postgres/pgvector and serves semantic search.
//
//	indexer [serve]                  consume repo.registered + serve HTTP (default)
//	indexer index  -url URL -name N  index a repository once and exit (demo/CLI)
//	indexer search -name N QUERY     query an indexed repository (demo/CLI)
//	indexer healthcheck              probe /healthz (Docker HEALTHCHECK)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/Shaj2x/devassist/libs/gocommon/config"
	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/libs/gocommon/health"
	"github.com/Shaj2x/devassist/libs/gocommon/httpserver"
	"github.com/Shaj2x/devassist/libs/gocommon/kafkax"
	"github.com/Shaj2x/devassist/libs/gocommon/logging"
	idxconfig "github.com/Shaj2x/devassist/services/indexer/internal/config"
	"github.com/Shaj2x/devassist/services/indexer/internal/consumer"
	"github.com/Shaj2x/devassist/services/indexer/internal/embed"
	"github.com/Shaj2x/devassist/services/indexer/internal/httpapi"
	"github.com/Shaj2x/devassist/services/indexer/internal/pipeline"
	"github.com/Shaj2x/devassist/services/indexer/internal/search"
	"github.com/Shaj2x/devassist/services/indexer/internal/store"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

const serviceName = "indexer"

func main() {
	os.Exit(run(os.Args[1:]))
}

// run returns the exit code, so deferred cleanup always runs before os.Exit.
func run(args []string) int {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	if cmd == "healthcheck" {
		return health.ProbeLocal(config.String("PORT", "8080"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx)
	case "index":
		err = indexOnce(ctx, args)
	case "search":
		err = searchOnce(ctx, args)
	default:
		err = fmt.Errorf("unknown command %q (want serve, index, search or healthcheck)", cmd)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", serviceName, err)
		return 1
	}
	return 0
}

// deps are the clients every command needs.
type deps struct {
	cfg      idxconfig.Config
	log      *slog.Logger
	store    *store.Store
	redis    *redis.Client
	embedder embed.Embedder
	search   *search.Service
	pipeline *pipeline.Pipeline
}

func setup(ctx context.Context) (*deps, error) {
	cfg, err := idxconfig.Load()
	if err != nil {
		return nil, err
	}
	log := logging.New(serviceName, cfg.LogLevel)

	st, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("redis url: %w", err)
	}
	rdb := redis.NewClient(redisOpts)

	var embedder embed.Embedder
	embedder, err = embed.New(embed.Options{
		Provider: cfg.EmbeddingProvider, Model: cfg.EmbeddingModel, Dimensions: idxconfig.Dimensions,
		OpenAIAPIKey: cfg.OpenAIAPIKey, OpenAIBaseURL: cfg.OpenAIBaseURL,
		VoyageAPIKey: cfg.VoyageAPIKey, VoyageBaseURL: cfg.VoyageBaseURL,
		HTTP: embed.HTTPOptions{Timeout: cfg.EmbeddingTimeout, MaxRetries: cfg.EmbeddingMaxRetries},
	})
	if err != nil {
		st.Close()
		_ = rdb.Close()
		return nil, err
	}
	if cfg.EmbeddingProvider != "local" {
		// One shared bucket across all embedding workers.
		embedder = embed.WithRateLimit(embedder, cfg.EmbeddingRPS, cfg.EmbeddingConcurrency)
	}

	svc := search.New(st, embedder, rdb, cfg.SearchCacheTTL, log)
	pl := pipeline.New(st, embedder, rdb, pipeline.Options{
		WorkDir: cfg.WorkDir, MaxFileBytes: cfg.MaxFileBytes, ChunkWorkers: cfg.ChunkWorkers,
		EmbedBatchSize: cfg.EmbeddingBatchSize, EmbedConcurrency: cfg.EmbeddingConcurrency,
		LockTTL: cfg.LockTTL, GitHubToken: cfg.GitHubToken,
	}, log)
	pl.OnIndexed = svc.Invalidate

	return &deps{cfg: cfg, log: log, store: st, redis: rdb, embedder: embedder, search: svc, pipeline: pl}, nil
}

func (d *deps) close() {
	d.store.Close()
	_ = d.redis.Close()
}

// serve runs the HTTP API and the repo.registered consumer until shutdown.
func serve(ctx context.Context) error {
	d, err := setup(ctx)
	if err != nil {
		return err
	}
	defer d.close()

	writer := kafkax.NewWriter(d.cfg.KafkaBrokers)
	defer writer.Close()
	reader := kafkax.NewReader(d.cfg.KafkaBrokers, d.cfg.KafkaGroupPrefix+"-indexer", string(events.RepoRegistered))
	defer reader.Close()

	handler := consumer.NewHandler(d.pipeline, d.store, kafkax.NewPublisher(writer, serviceName), d.log)
	// Indexing can take a while; back off generously when another worker
	// holds the repository lock.
	cons := kafkax.NewConsumer(reader, writer, handler.Handle, d.log, kafkax.ConsumerConfig{MaxAttempts: 6})

	mux := http.NewServeMux()
	health.Register(mux, map[string]health.Check{
		"postgres": health.Postgres(d.store.Pool()),
		"redis":    health.Redis(d.redis),
		"kafka":    health.Kafka(d.cfg.KafkaBrokers, events.TopicNames()),
	}, d.cfg.ReadinessTimeout)
	httpapi.Register(mux, d.search, d.log)

	d.log.InfoContext(ctx, "starting", "port", d.cfg.Port, "embedding_model", d.embedder.Model())
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return httpserver.Run(ctx, d.log, ":"+strconv.Itoa(d.cfg.Port), mux, d.cfg.ShutdownGracePeriod)
	})
	g.Go(func() error { return cons.Run(ctx) })
	return g.Wait()
}

// indexOnce registers (if needed) and indexes a repository synchronously.
// It is a developer convenience for demos; production indexing is driven by
// repo.registered events from the api.
func indexOnce(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	url := fs.String("url", "", "clone URL (https://, file://, or a path)")
	name := fs.String("name", "", "repository name, e.g. demo/python-dateutils")
	branch := fs.String("branch", "main", "branch to index when -sha is empty")
	sha := fs.String("sha", "", "exact commit to index")
	_ = fs.Parse(args)
	if *url == "" || *name == "" {
		return errors.New("index: -url and -name are required")
	}

	d, err := setup(ctx)
	if err != nil {
		return err
	}
	defer d.close()

	repoID, err := d.store.EnsureDevRepository(ctx, *name, *url, *branch)
	if err != nil {
		return err
	}
	ctx = logging.WithTraceID(ctx, repoID)
	res, err := d.pipeline.Index(ctx, pipeline.Request{RepoID: repoID, CloneURL: *url, Branch: *branch, CommitSHA: *sha})
	if err != nil {
		return err
	}
	fmt.Printf("repo_id=%s snapshot_id=%s commit=%s files=%d chunks=%d embedded=%d reused=%d skipped=%v took=%s\n",
		repoID, res.Snapshot.ID, res.Snapshot.CommitSHA, res.Files, res.Chunks, res.Embedded, res.Reused,
		res.Skipped, res.Duration.Round(1e6))
	return nil
}

// searchOnce prints the top results for a query (or a symbol lookup).
func searchOnce(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	name := fs.String("name", "", "repository name")
	repoID := fs.String("repo-id", "", "repository id (instead of -name)")
	k := fs.Int("k", 5, "number of results")
	symbol := fs.Bool("symbol", false, "look up a symbol by name instead of semantic search")
	_ = fs.Parse(args)
	query := strings.Join(fs.Args(), " ")
	if query == "" || (*name == "" && *repoID == "") {
		return errors.New(`search: usage: search -name REPO [-k 5] [-symbol] "query"`)
	}

	d, err := setup(ctx)
	if err != nil {
		return err
	}
	defer d.close()
	if *repoID == "" {
		if *repoID, err = d.store.RepositoryByName(ctx, *name); err != nil {
			return fmt.Errorf("repository %q: %w", *name, err)
		}
	}

	var resp search.Response
	if *symbol {
		resp, err = d.search.Symbols(ctx, *repoID, query, *k)
	} else {
		resp, err = d.search.Search(ctx, *repoID, query, *k)
	}
	if err != nil {
		return err
	}
	fmt.Printf("query: %q  (commit %s, %d results)\n\n", query, short(resp.CommitSHA), len(resp.Results))
	for i, h := range resp.Results {
		label := h.SymbolKind
		if h.SymbolName != "" {
			label += " " + h.SymbolName
		}
		fmt.Printf("%d. %s:%d-%d  [%s]  score=%.3f\n", i+1, h.FilePath, h.StartLine, h.EndLine, label, h.Score)
		lines := strings.Split(h.Content, "\n")
		for _, l := range lines[:min(len(lines), 4)] {
			fmt.Printf("     %s\n", l)
		}
		if len(lines) > 4 {
			fmt.Printf("     ... (%d more lines)\n", len(lines)-4)
		}
		fmt.Println()
	}
	return nil
}

func short(sha string) string { return sha[:min(len(sha), 10)] }
