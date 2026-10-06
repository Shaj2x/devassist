package search

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Shaj2x/devassist/services/indexer/internal/embed"
	"github.com/Shaj2x/devassist/services/indexer/internal/store"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type fakeStore struct {
	snap         store.Snapshot
	snapErr      error
	searchCalls  int
	snapshotHits int
}

func (f *fakeStore) LatestReadySnapshot(context.Context, string) (store.Snapshot, error) {
	f.snapshotHits++
	return f.snap, f.snapErr
}

func (f *fakeStore) Search(_ context.Context, _ string, q []float32, k int) ([]store.Hit, error) {
	f.searchCalls++
	if len(q) != 16 {
		return nil, errors.New("query not embedded")
	}
	return []store.Hit{{FilePath: "a.py", StartLine: 1, EndLine: 2, Score: 0.9}}[:min(k, 1)], nil
}

func (f *fakeStore) Symbols(context.Context, string, string, int) ([]store.Hit, error) {
	return nil, nil
}

func newService(t *testing.T, st Store) (*Service, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return New(st, embed.NewLocal(16), rdb, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil))), mr
}

func TestSearchCachesResultsPerSnapshot(t *testing.T) {
	st := &fakeStore{snap: store.Snapshot{ID: "snap-1", CommitSHA: "abc"}}
	svc, _ := newService(t, st)
	ctx := context.Background()

	first, err := svc.Search(ctx, "repo", "leap year", 5)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := svc.Search(ctx, "repo", "leap year", 5)
	if first.Cached || !second.Cached || st.searchCalls != 1 {
		t.Fatalf("cached=%v,%v searchCalls=%d", first.Cached, second.Cached, st.searchCalls)
	}
	if st.snapshotHits != 1 {
		t.Errorf("snapshot pointer not cached: %d lookups", st.snapshotHits)
	}

	// A reindex points at a new snapshot; old cached results must not leak.
	st.snap = store.Snapshot{ID: "snap-2", CommitSHA: "def"}
	svc.Invalidate(ctx, "repo")
	third, _ := svc.Search(ctx, "repo", "leap year", 5)
	if third.Cached || third.SnapshotID != "snap-2" {
		t.Fatalf("after reindex: cached=%v snapshot=%s", third.Cached, third.SnapshotID)
	}
}

func TestRedisOutageDegradesGracefully(t *testing.T) {
	st := &fakeStore{snap: store.Snapshot{ID: "s"}}
	svc, mr := newService(t, st)
	mr.Close()
	resp, err := svc.Search(context.Background(), "repo", "q", 3)
	if err != nil || len(resp.Results) != 1 {
		t.Fatalf("search failed without redis: %v %v", resp, err)
	}
}

func TestNotIndexed(t *testing.T) {
	svc, _ := newService(t, &fakeStore{snapErr: store.ErrNotFound})
	if _, err := svc.Search(context.Background(), "repo", "q", 3); !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("got %v, want ErrNotIndexed", err)
	}
}
