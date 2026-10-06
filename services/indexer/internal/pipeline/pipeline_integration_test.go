//go:build integration

// Integration tests: run against the Postgres and Redis from `make up`.
//
//	go test -tags integration ./...
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shaj2x/devassist/services/indexer/internal/embed"
	"github.com/Shaj2x/devassist/services/indexer/internal/search"
	"github.com/Shaj2x/devassist/services/indexer/internal/store"
	"github.com/redis/go-redis/v9"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// countingEmbedder records how many texts were sent to the "provider".
type countingEmbedder struct {
	embed.Embedder
	texts atomic.Int64
	fail  atomic.Bool
	delay time.Duration
}

func (c *countingEmbedder) Embed(ctx context.Context, texts []string, t embed.InputType) ([][]float32, error) {
	time.Sleep(c.delay)
	if c.fail.Load() {
		return nil, errors.New("provider quota exceeded")
	}
	c.texts.Add(int64(len(texts)))
	return c.Embedder.Embed(ctx, texts, t)
}

type fixture struct {
	st       *store.Store
	rdb      *redis.Client
	embedder *countingEmbedder
	pipe     *Pipeline
	repoDir  string
	repoURL  string
	repoID   string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Connect(ctx, env("DATABASE_URL", "postgresql://devassist:devassist@localhost:5432/devassist"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	opts, _ := redis.ParseURL(env("REDIS_URL", "redis://localhost:6379/0"))
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })

	f := &fixture{st: st, rdb: rdb, embedder: &countingEmbedder{Embedder: embed.NewLocal(1024)}}
	f.pipe = New(st, f.embedder, rdb, Options{
		WorkDir: t.TempDir(), MaxFileBytes: 1 << 20, ChunkWorkers: 4, EmbedBatchSize: 4, EmbedConcurrency: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	f.repoDir = t.TempDir()
	f.git(t, "init", "--quiet", "-b", "main")
	f.write(t, "billing/money.py", "def format_money(cents):\n    return f'${cents / 100:.2f}'\n")
	f.write(t, "auth/login.py", "def login(user, password):\n    return check_password(user, password)\n\n\ndef logout(session):\n    session.clear()\n")
	f.write(t, "util/dates.go", "package util\n\n// IsLeapYear reports leap years.\nfunc IsLeapYear(y int) bool { return y%4 == 0 }\n")
	f.commit(t, "initial")
	f.repoURL = "file://" + f.repoDir

	name := fmt.Sprintf("it/%d", time.Now().UnixNano())
	if f.repoID, err = st.EnsureDevRepository(ctx, name, f.repoURL, "main"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(context.Background(), `DELETE FROM repositories WHERE id = $1`, f.repoID)
	})
	return f
}

func (f *fixture) write(t *testing.T, rel, content string) {
	t.Helper()
	path := filepath.Join(f.repoDir, rel)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.repoDir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) commit(t *testing.T, msg string) string {
	f.git(t, "add", "-A")
	f.git(t, "commit", "--quiet", "-m", msg)
	return f.git(t, "rev-parse", "HEAD")
}

func (f *fixture) countChunks(t *testing.T) (total, snapshots int) {
	t.Helper()
	err := f.st.Pool().QueryRow(context.Background(),
		`SELECT count(*), count(DISTINCT snapshot_id) FROM code_chunks WHERE repo_id = $1`, f.repoID).Scan(&total, &snapshots)
	if err != nil {
		t.Fatal(err)
	}
	return total, snapshots
}

func TestIndexSearchAndIncrementalReindex(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	first, err := f.pipe.Index(ctx, Request{RepoID: f.repoID, CloneURL: f.repoURL, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Files != 3 || first.Chunks != 5 || first.Embedded != 5 || first.Reused != 0 {
		t.Fatalf("first run: %+v", first)
	}

	svc := search.New(f.st, f.embedder, f.rdb, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	resp, err := svc.Search(ctx, f.repoID, "user login password", 2)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Results[0].SymbolName != "login" || resp.Results[0].StartLine != 1 || resp.Results[0].EndLine != 2 {
		t.Fatalf("top hit = %+v", resp.Results[0])
	}
	sym, err := svc.Symbols(ctx, f.repoID, "IsLeap", 5)
	if err != nil || len(sym.Results) != 1 || sym.Results[0].FilePath != "util/dates.go" {
		t.Fatalf("symbols = %+v, %v", sym.Results, err)
	}

	// Same commit again: nothing to do.
	again, err := f.pipe.Index(ctx, Request{RepoID: f.repoID, CloneURL: f.repoURL, Branch: "main"})
	if err != nil || !again.Skipped {
		t.Fatalf("re-index of same commit: %+v %v", again, err)
	}

	// Change one function: only it is re-embedded; the rest is reused.
	f.write(t, "billing/money.py", "def format_money(cents, currency='$'):\n    return f'{currency}{cents / 100:.2f}'\n")
	f.commit(t, "currency")
	before := f.embedder.texts.Load()
	second, err := f.pipe.Index(ctx, Request{RepoID: f.repoID, CloneURL: f.repoURL, Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Embedded != 1 || second.Reused != 4 || f.embedder.texts.Load()-before != 1 {
		t.Fatalf("incremental run: %+v (provider saw %d texts)", second, f.embedder.texts.Load()-before)
	}
	if total, snaps := f.countChunks(t); total != 5 || snaps != 1 {
		t.Fatalf("old snapshot chunks not cleaned up: %d chunks in %d snapshots", total, snaps)
	}

	// The cached snapshot pointer was invalidated? Search must see the new commit.
	svc.Invalidate(ctx, f.repoID)
	resp, _ = svc.Search(ctx, f.repoID, "format money currency", 1)
	if resp.SnapshotID != second.Snapshot.ID || !strings.Contains(resp.Results[0].Content, "currency") {
		t.Fatalf("search still on old snapshot: %+v", resp)
	}
}

func TestConcurrentIndexOfSameRepoIsSerialized(t *testing.T) {
	f := setup(t)
	f.embedder.delay = 200 * time.Millisecond // keep the first run busy

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.pipe.Index(context.Background(), Request{RepoID: f.repoID, CloneURL: f.repoURL, Branch: "main"})
		}()
	}
	wg.Wait()

	busy := 0
	for _, err := range errs {
		if errors.Is(err, ErrBusy) {
			busy++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if busy != 1 {
		t.Fatalf("want exactly one ErrBusy, got errs=%v", errs)
	}
}

func TestEmbeddingFailureMarksSnapshotFailed(t *testing.T) {
	f := setup(t)
	f.embedder.fail.Store(true)

	res, err := f.pipe.Index(context.Background(), Request{RepoID: f.repoID, CloneURL: f.repoURL, Branch: "main"})
	if err == nil || res.Snapshot.ID == "" {
		t.Fatalf("want failure with a snapshot, got %+v %v", res, err)
	}
	var status, reason string
	_ = f.st.Pool().QueryRow(context.Background(),
		`SELECT status, coalesce(error, '') FROM repo_snapshots WHERE id = $1`, res.Snapshot.ID).Scan(&status, &reason)
	if status != "failed" || !strings.Contains(reason, "quota") {
		t.Fatalf("snapshot status=%s error=%q", status, reason)
	}

	// A retry after the provider recovers resets the same snapshot row.
	f.embedder.fail.Store(false)
	res2, err := f.pipe.Index(context.Background(), Request{RepoID: f.repoID, CloneURL: f.repoURL, Branch: "main"})
	if err != nil || res2.Snapshot.ID != res.Snapshot.ID || res2.Snapshot.Status != "ready" {
		t.Fatalf("retry: %+v %v", res2, err)
	}
}
