// Package search answers semantic and symbol queries against a repository's
// newest ready snapshot, with Redis caching.
//
// Cache keys include the snapshot id, so a reindex naturally invalidates
// search results; only the small "latest snapshot" pointer needs an explicit
// delete, which the pipeline triggers via Invalidate.
package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Shaj2x/devassist/services/indexer/internal/embed"
	"github.com/Shaj2x/devassist/services/indexer/internal/store"
	"github.com/redis/go-redis/v9"
)

// ErrNotIndexed means the repository has no ready snapshot yet.
var ErrNotIndexed = errors.New("repository has no ready index")

// Store is the subset of *store.Store search needs.
type Store interface {
	LatestReadySnapshot(ctx context.Context, repoID string) (store.Snapshot, error)
	Search(ctx context.Context, snapshotID string, query []float32, k int) ([]store.Hit, error)
	Symbols(ctx context.Context, snapshotID, name string, limit int) ([]store.Hit, error)
}

// Response is returned by both search and symbol lookup.
type Response struct {
	RepoID     string      `json:"repo_id"`
	SnapshotID string      `json:"snapshot_id"`
	CommitSHA  string      `json:"commit_sha"`
	Results    []store.Hit `json:"results"`
	Cached     bool        `json:"cached"`
}

type Service struct {
	store       Store
	embedder    embed.Embedder
	redis       *redis.Client // nil disables caching
	resultTTL   time.Duration
	snapshotTTL time.Duration
	log         *slog.Logger
}

func New(st Store, e embed.Embedder, rdb *redis.Client, resultTTL time.Duration, log *slog.Logger) *Service {
	return &Service{store: st, embedder: e, redis: rdb, resultTTL: resultTTL, snapshotTTL: 5 * time.Minute, log: log}
}

func snapshotKey(repoID string) string { return "devassist:repo:" + repoID + ":latest-snapshot" }

// Search embeds the query and returns the k nearest chunks.
func (s *Service) Search(ctx context.Context, repoID, query string, k int) (Response, error) {
	snap, err := s.latestSnapshot(ctx, repoID)
	if err != nil {
		return Response{}, err
	}
	key := fmt.Sprintf("devassist:search:%s:%s", snap.ID, digest(s.embedder.Model(), fmt.Sprint(k), query))
	if resp, ok := s.cached(ctx, key); ok {
		return resp, nil
	}
	vecs, err := s.embedder.Embed(ctx, []string{query}, embed.Query)
	if err != nil {
		return Response{}, fmt.Errorf("embed query: %w", err)
	}
	hits, err := s.store.Search(ctx, snap.ID, vecs[0], k)
	if err != nil {
		return Response{}, err
	}
	resp := Response{RepoID: repoID, SnapshotID: snap.ID, CommitSHA: snap.CommitSHA, Results: nonNil(hits)}
	s.cacheSet(ctx, key, resp, s.resultTTL)
	return resp, nil
}

// Symbols looks up definitions by name.
func (s *Service) Symbols(ctx context.Context, repoID, name string, limit int) (Response, error) {
	snap, err := s.latestSnapshot(ctx, repoID)
	if err != nil {
		return Response{}, err
	}
	hits, err := s.store.Symbols(ctx, snap.ID, name, limit)
	if err != nil {
		return Response{}, err
	}
	return Response{RepoID: repoID, SnapshotID: snap.ID, CommitSHA: snap.CommitSHA, Results: nonNil(hits)}, nil
}

// LatestSnapshot exposes the snapshot pointer (for the status endpoint).
func (s *Service) LatestSnapshot(ctx context.Context, repoID string) (store.Snapshot, error) {
	return s.latestSnapshot(ctx, repoID)
}

// Invalidate drops the cached snapshot pointer after a reindex.
func (s *Service) Invalidate(ctx context.Context, repoID string) {
	if s.redis == nil {
		return
	}
	if err := s.redis.Del(ctx, snapshotKey(repoID)).Err(); err != nil {
		s.log.WarnContext(ctx, "cache invalidation failed", "repo_id", repoID, "error", err)
	}
}

func (s *Service) latestSnapshot(ctx context.Context, repoID string) (store.Snapshot, error) {
	if s.redis != nil {
		if raw, err := s.redis.Get(ctx, snapshotKey(repoID)).Bytes(); err == nil {
			var snap store.Snapshot
			if json.Unmarshal(raw, &snap) == nil {
				return snap, nil
			}
		}
	}
	snap, err := s.store.LatestReadySnapshot(ctx, repoID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Snapshot{}, ErrNotIndexed
	}
	if err != nil {
		return store.Snapshot{}, err
	}
	s.cacheSet(ctx, snapshotKey(repoID), snap, s.snapshotTTL)
	return snap, nil
}

// cached and cacheSet treat Redis as optional: an outage degrades to
// uncached queries instead of failing them.
func (s *Service) cached(ctx context.Context, key string) (Response, bool) {
	if s.redis == nil {
		return Response{}, false
	}
	raw, err := s.redis.Get(ctx, key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			s.log.WarnContext(ctx, "cache read failed", "error", err)
		}
		return Response{}, false
	}
	var resp Response
	if json.Unmarshal(raw, &resp) != nil {
		return Response{}, false
	}
	resp.Cached = true
	return resp, true
}

func (s *Service) cacheSet(ctx context.Context, key string, v any, ttl time.Duration) {
	if s.redis == nil {
		return
	}
	raw, err := json.Marshal(v)
	if err == nil {
		err = s.redis.Set(ctx, key, raw, ttl).Err()
	}
	if err != nil {
		s.log.WarnContext(ctx, "cache write failed", "error", err)
	}
}

func digest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func nonNil(h []store.Hit) []store.Hit {
	if h == nil {
		return []store.Hit{}
	}
	return h
}
