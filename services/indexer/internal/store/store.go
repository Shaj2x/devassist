// Package store is the indexer's Postgres access layer: snapshots, chunks
// with their pgvector embeddings, search, and the idempotency ledger.
//
// The schema is owned by Alembic (services/api/alembic); this package only
// reads and writes rows.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

// Store wraps a connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// Connect opens a pool and registers the pgvector type on every connection.
func Connect(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.AfterConnect = pgxvec.RegisterTypes
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Pool() *pgxpool.Pool { return s.pool }
func (s *Store) Close()              { s.pool.Close() }

// ---------------------------------------------------------------------------
// Repositories and snapshots
// ---------------------------------------------------------------------------

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

type Snapshot struct {
	ID             string
	RepoID         string
	CommitSHA      string
	Status         string
	EmbeddingModel string
	FileCount      int
	ChunkCount     int
	Error          string
	FinishedAt     *time.Time
}

const snapshotCols = `id::text, repo_id::text, commit_sha, status, coalesce(embedding_model, ''),
	file_count, chunk_count, coalesce(error, ''), finished_at`

func scanSnapshot(row pgx.Row) (Snapshot, error) {
	var s Snapshot
	err := row.Scan(&s.ID, &s.RepoID, &s.CommitSHA, &s.Status, &s.EmbeddingModel,
		&s.FileCount, &s.ChunkCount, &s.Error, &s.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}
	return s, err
}

// RepositoryExists reports whether a repositories row exists.
func (s *Store) RepositoryExists(ctx context.Context, repoID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM repositories WHERE id = $1)`, repoID).Scan(&exists)
	if isInvalidUUID(err) {
		return false, nil
	}
	return exists, err
}

// EnsureDevRepository creates (or finds) a repository owned by a local "demo"
// user. It exists for the CLI demo only; in the running system the api
// service owns repository registration.
func (s *Store) EnsureDevRepository(ctx context.Context, fullName, cloneURL, branch string) (string, error) {
	var repoID string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var userID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO users (github_login) VALUES ('demo')
			ON CONFLICT (github_login) DO UPDATE SET updated_at = now()
			RETURNING id::text`).Scan(&userID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO repositories (owner_id, full_name, clone_url, default_branch)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (owner_id, full_name)
			DO UPDATE SET clone_url = EXCLUDED.clone_url, default_branch = EXCLUDED.default_branch, updated_at = now()
			RETURNING id::text`, userID, fullName, cloneURL, branch).Scan(&repoID)
	})
	return repoID, err
}

// RepositoryByName finds a repository id by "owner/name" (CLI convenience).
func (s *Store) RepositoryByName(ctx context.Context, fullName string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT id::text FROM repositories WHERE full_name = $1 ORDER BY created_at LIMIT 1`, fullName).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// GetSnapshot returns the snapshot for (repo, sha) or ErrNotFound.
func (s *Store) GetSnapshot(ctx context.Context, repoID, sha string) (Snapshot, error) {
	return scanSnapshot(s.pool.QueryRow(ctx,
		`SELECT `+snapshotCols+` FROM repo_snapshots WHERE repo_id = $1 AND commit_sha = $2`, repoID, sha))
}

// LatestReadySnapshot returns the most recently finished ready snapshot.
func (s *Store) LatestReadySnapshot(ctx context.Context, repoID string) (Snapshot, error) {
	snap, err := scanSnapshot(s.pool.QueryRow(ctx, `
		SELECT `+snapshotCols+` FROM repo_snapshots
		WHERE repo_id = $1 AND status = 'ready'
		ORDER BY finished_at DESC NULLS LAST LIMIT 1`, repoID))
	if isInvalidUUID(err) {
		return Snapshot{}, ErrNotFound
	}
	return snap, err
}

// StartSnapshot creates the snapshot row for (repo, sha), or resets an
// existing failed/interrupted one, and deletes any chunks a previous partial
// run left behind. Safe to call repeatedly for the same commit.
func (s *Store) StartSnapshot(ctx context.Context, repoID, sha, branch, model string) (Snapshot, error) {
	var snap Snapshot
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		snap, err = scanSnapshot(tx.QueryRow(ctx, `
			INSERT INTO repo_snapshots (repo_id, commit_sha, branch, embedding_model, status, started_at)
			VALUES ($1, $2, nullif($3, ''), $4, 'indexing', now())
			ON CONFLICT (repo_id, commit_sha) DO UPDATE SET
				status = 'indexing', embedding_model = EXCLUDED.embedding_model, error = NULL,
				file_count = 0, chunk_count = 0, started_at = now(), finished_at = NULL, updated_at = now()
			RETURNING `+snapshotCols, repoID, sha, branch, model))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM code_chunks WHERE snapshot_id = $1`, snap.ID)
		return err
	})
	return snap, err
}

// FinishSnapshot marks a snapshot ready and drops chunks of the repo's older
// snapshots: search always reads the newest index, and the embeddings worth
// reusing were already copied into this snapshot.
func (s *Store) FinishSnapshot(ctx context.Context, snap Snapshot, files, chunks int) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE repo_snapshots SET status = 'ready', file_count = $2, chunk_count = $3,
				finished_at = now(), updated_at = now()
			WHERE id = $1`, snap.ID, files, chunks); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM code_chunks WHERE repo_id = $1 AND snapshot_id <> $2`, snap.RepoID, snap.ID)
		return err
	})
}

// FailSnapshot records why indexing failed.
func (s *Store) FailSnapshot(ctx context.Context, snapshotID, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE repo_snapshots SET status = 'failed', error = $2, finished_at = now(), updated_at = now()
		WHERE id = $1`, snapshotID, reason)
	return err
}

// ---------------------------------------------------------------------------
// Chunks
// ---------------------------------------------------------------------------

// ChunkRow is one row for code_chunks.
type ChunkRow struct {
	FilePath    string
	Language    string
	SymbolName  string
	SymbolKind  string
	StartLine   int
	EndLine     int
	Content     string
	ContentHash string
	TokenCount  int
	Embedding   []float32
}

// ReusableEmbeddings returns stored embeddings, keyed by content hash, for
// any of the given hashes already embedded for this repo with this model.
func (s *Store) ReusableEmbeddings(ctx context.Context, repoID, model string, hashes []string) (map[string][]float32, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (c.content_hash) c.content_hash, c.embedding
		FROM code_chunks c JOIN repo_snapshots s ON s.id = c.snapshot_id
		WHERE c.repo_id = $1 AND s.embedding_model = $2
		  AND c.content_hash = ANY($3) AND c.embedding IS NOT NULL`, repoID, model, hashes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]float32)
	for rows.Next() {
		var hash string
		var vec pgvector.Vector
		if err := rows.Scan(&hash, &vec); err != nil {
			return nil, err
		}
		out[hash] = vec.Slice()
	}
	return out, rows.Err()
}

// InsertChunks bulk-loads chunks with COPY, the fastest way into Postgres.
func (s *Store) InsertChunks(ctx context.Context, snap Snapshot, chunks []ChunkRow) error {
	_, err := s.pool.CopyFrom(ctx,
		pgx.Identifier{"code_chunks"},
		[]string{"snapshot_id", "repo_id", "file_path", "language", "symbol_name", "symbol_kind",
			"start_line", "end_line", "content", "content_hash", "token_count", "embedding"},
		pgx.CopyFromSlice(len(chunks), func(i int) ([]any, error) {
			c := chunks[i]
			return []any{snap.ID, snap.RepoID, c.FilePath, nullable(c.Language), nullable(c.SymbolName),
				nullable(c.SymbolKind), c.StartLine, c.EndLine, c.Content, c.ContentHash, c.TokenCount,
				pgvector.NewVector(c.Embedding)}, nil
		}))
	return err
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// Hit is one search or symbol result.
type Hit struct {
	FilePath   string  `json:"file_path"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	SymbolName string  `json:"symbol_name,omitempty"`
	SymbolKind string  `json:"symbol_kind,omitempty"`
	Language   string  `json:"language,omitempty"`
	Score      float64 `json:"score,omitempty"`
	Content    string  `json:"content"`
}

const hitCols = `file_path, start_line, end_line, coalesce(symbol_name, ''), coalesce(symbol_kind, ''),
	coalesce(language, ''), content`

// Search returns the k chunks of a snapshot nearest to the query vector by
// cosine distance. Iterative HNSW scanning keeps returning candidates until k
// rows survive the snapshot filter.
func (s *Store) Search(ctx context.Context, snapshotID string, query []float32, k int) ([]Hit, error) {
	var hits []Hit
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT `+hitCols+`, 1 - (embedding <=> $2) AS score
			FROM code_chunks
			WHERE snapshot_id = $1
			ORDER BY embedding <=> $2
			LIMIT $3`, snapshotID, pgvector.NewVector(query), k)
		if err != nil {
			return err
		}
		hits, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Hit, error) {
			var h Hit
			err := row.Scan(&h.FilePath, &h.StartLine, &h.EndLine, &h.SymbolName, &h.SymbolKind,
				&h.Language, &h.Content, &h.Score)
			return h, err
		})
		return err
	})
	return hits, err
}

// Symbols finds definitions by name: exact matches first, then
// case-insensitive prefix matches, then qualified names (Class.method).
func (s *Store) Symbols(ctx context.Context, snapshotID, name string, limit int) ([]Hit, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+hitCols+` FROM code_chunks
		WHERE snapshot_id = $1 AND symbol_name IS NOT NULL AND (
			symbol_name = $2
			OR symbol_name ILIKE $3 ESCAPE '\'
			OR symbol_name ILIKE $4 ESCAPE '\')
		ORDER BY (symbol_name = $2) DESC, (symbol_name ILIKE $3 ESCAPE '\') DESC,
		         length(symbol_name), file_path, start_line
		LIMIT $5`,
		snapshotID, name, likeEscape(name)+"%", "%."+likeEscape(name)+"%", limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Hit, error) {
		var h Hit
		err := row.Scan(&h.FilePath, &h.StartLine, &h.EndLine, &h.SymbolName, &h.SymbolKind, &h.Language, &h.Content)
		return h, err
	})
}

// ---------------------------------------------------------------------------
// Idempotency ledger
// ---------------------------------------------------------------------------

// EventProcessed reports whether consumer already handled eventID.
func (s *Store) EventProcessed(ctx context.Context, consumer, eventID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM processed_events WHERE consumer = $1 AND event_id = $2)`,
		consumer, eventID).Scan(&exists)
	return exists, err
}

// MarkEventProcessed records eventID as handled. Duplicates are ignored.
func (s *Store) MarkEventProcessed(ctx context.Context, consumer, eventID, eventType string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO processed_events (consumer, event_id, event_type) VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, consumer, eventID, eventType)
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func likeEscape(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '%' || r == '_' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}

// isInvalidUUID: a malformed id from a client is "not found", not a 500.
func isInvalidUUID(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "22P02"
}
