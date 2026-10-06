// Package pipeline indexes one repository commit end to end:
//
//	lock repo -> clone at commit -> skip if already indexed -> walk files
//	-> chunk (worker pool) -> reuse unchanged embeddings -> embed the rest
//	(worker pool, rate limited, batched) -> COPY into Postgres -> mark ready
//
// Every step is safe to repeat: snapshots are unique per (repo, commit), a
// restarted run deletes its own partial chunks first, and a Redis lock stops
// two workers indexing the same repository at the same time.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/Shaj2x/devassist/libs/gocommon/lock"
	"github.com/Shaj2x/devassist/services/indexer/internal/chunker"
	"github.com/Shaj2x/devassist/services/indexer/internal/embed"
	"github.com/Shaj2x/devassist/services/indexer/internal/gitrepo"
	"github.com/Shaj2x/devassist/services/indexer/internal/store"
	"github.com/Shaj2x/devassist/services/indexer/internal/walker"
	"golang.org/x/sync/errgroup"
)

// ErrBusy means another worker holds this repository's lock. Callers retry.
var ErrBusy = errors.New("repository is being indexed by another worker")

// maxEmbedChars keeps every input under provider token limits (~8k tokens).
const maxEmbedChars = 24000

// Options sizes the worker pools.
type Options struct {
	WorkDir          string
	MaxFileBytes     int
	ChunkWorkers     int
	EmbedBatchSize   int
	EmbedConcurrency int
	LockTTL          time.Duration
	GitHubToken      string
}

// Request identifies what to index.
type Request struct {
	RepoID    string
	CloneURL  string
	Branch    string
	CommitSHA string // empty means the head of Branch
}

// Result summarizes a run.
type Result struct {
	Snapshot store.Snapshot
	Files    int
	Chunks   int
	Embedded int  // chunks sent to the embedding provider
	Reused   int  // chunks whose embedding was reused from an earlier commit
	Skipped  bool // this commit was already indexed with this model
	Duration time.Duration
}

// Pipeline is safe for concurrent use across different repositories.
type Pipeline struct {
	store    *store.Store
	embedder embed.Embedder
	chunker  *chunker.Chunker
	locker   lock.Client
	opts     Options
	log      *slog.Logger
	// OnIndexed runs after a snapshot becomes ready (used to drop caches).
	OnIndexed func(ctx context.Context, repoID string)
}

func New(st *store.Store, e embed.Embedder, locker lock.Client, opts Options, log *slog.Logger) *Pipeline {
	if opts.ChunkWorkers < 1 {
		opts.ChunkWorkers = 1
	}
	if opts.EmbedConcurrency < 1 {
		opts.EmbedConcurrency = 1
	}
	if opts.EmbedBatchSize < 1 {
		opts.EmbedBatchSize = 64
	}
	if opts.LockTTL <= 0 {
		opts.LockTTL = 2 * time.Minute
	}
	return &Pipeline{store: st, embedder: e, chunker: chunker.New(chunker.Options{}), locker: locker, opts: opts, log: log}
}

// Index runs the pipeline. On a failure after the snapshot row exists, the
// snapshot is marked failed and Result.Snapshot is still populated, so the
// caller can report it.
func (p *Pipeline) Index(ctx context.Context, req Request) (Result, error) {
	started := time.Now()

	l, err := lock.Acquire(ctx, p.locker, "index:repo:"+req.RepoID, p.opts.LockTTL)
	if errors.Is(err, lock.ErrNotAcquired) {
		return Result{}, ErrBusy
	}
	if err != nil {
		return Result{}, fmt.Errorf("acquire lock: %w", err)
	}
	l.KeepAlive(ctx)
	defer func() { _ = l.Release(context.WithoutCancel(ctx)) }()

	if err := os.MkdirAll(p.opts.WorkDir, 0o750); err != nil {
		return Result{}, err
	}
	dir, err := os.MkdirTemp(p.opts.WorkDir, "repo-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)

	co, err := gitrepo.Clone(ctx, req.CloneURL, dir, gitrepo.Options{
		Branch: req.Branch, CommitSHA: req.CommitSHA, GitHubToken: p.opts.GitHubToken,
	})
	if err != nil {
		return Result{}, fmt.Errorf("checkout: %w", err)
	}

	existing, err := p.store.GetSnapshot(ctx, req.RepoID, co.CommitSHA)
	switch {
	case err == nil && existing.Status == "ready" && existing.EmbeddingModel == p.embedder.Model():
		p.log.InfoContext(ctx, "commit already indexed", "commit", co.CommitSHA, "snapshot_id", existing.ID)
		return Result{Snapshot: existing, Files: existing.FileCount, Chunks: existing.ChunkCount,
			Skipped: true, Duration: time.Since(started)}, nil
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return Result{}, err
	}

	snap, err := p.store.StartSnapshot(ctx, req.RepoID, co.CommitSHA, req.Branch, p.embedder.Model())
	if err != nil {
		return Result{}, fmt.Errorf("start snapshot: %w", err)
	}
	res, err := p.build(ctx, snap, co.Dir)
	res.Snapshot, res.Duration = snap, time.Since(started)
	if err != nil {
		reason := err.Error()
		if ferr := p.store.FailSnapshot(context.WithoutCancel(ctx), snap.ID, reason); ferr != nil {
			p.log.ErrorContext(ctx, "could not mark snapshot failed", "error", ferr)
		}
		res.Snapshot.Status, res.Snapshot.Error = "failed", reason
		return res, err
	}
	if err := p.store.FinishSnapshot(ctx, snap, res.Files, res.Chunks); err != nil {
		return res, fmt.Errorf("finish snapshot: %w", err)
	}
	res.Snapshot.Status, res.Snapshot.FileCount, res.Snapshot.ChunkCount = "ready", res.Files, res.Chunks
	if p.OnIndexed != nil {
		p.OnIndexed(ctx, req.RepoID)
	}
	p.log.InfoContext(ctx, "repository indexed", "commit", co.CommitSHA, "files", res.Files,
		"chunks", res.Chunks, "embedded", res.Embedded, "reused", res.Reused,
		"duration_ms", time.Since(started).Milliseconds())
	return res, nil
}

func (p *Pipeline) build(ctx context.Context, snap store.Snapshot, dir string) (Result, error) {
	files, err := walker.Walk(dir, p.opts.MaxFileBytes)
	if err != nil {
		return Result{}, fmt.Errorf("walk: %w", err)
	}
	chunks, err := p.chunkAll(ctx, files)
	if err != nil {
		return Result{}, err
	}

	// One embedding per distinct input text, however many chunks share it.
	rows := make([]store.ChunkRow, len(chunks))
	texts := make(map[string]string, len(chunks)) // hash -> text
	for i, c := range chunks {
		hash := c.Hash()
		rows[i] = store.ChunkRow{
			FilePath: c.FilePath, Language: c.Language, SymbolName: c.SymbolName, SymbolKind: c.SymbolKind,
			StartLine: c.StartLine, EndLine: c.EndLine, Content: c.Content, ContentHash: hash,
			TokenCount: c.TokenEstimate(),
		}
		texts[hash] = truncateRunes(c.EmbeddingText(), maxEmbedChars)
	}
	hashes := make([]string, 0, len(texts))
	for h := range texts {
		hashes = append(hashes, h)
	}

	vectors, err := p.store.ReusableEmbeddings(ctx, snap.RepoID, p.embedder.Model(), hashes)
	if err != nil {
		return Result{}, fmt.Errorf("load reusable embeddings: %w", err)
	}
	var missing []string
	for _, h := range hashes {
		if _, ok := vectors[h]; !ok {
			missing = append(missing, h)
		}
	}
	if err := p.embedAll(ctx, missing, texts, vectors); err != nil {
		return Result{}, err
	}

	embedded, reused := 0, 0
	missingSet := make(map[string]bool, len(missing))
	for _, h := range missing {
		missingSet[h] = true
	}
	for i := range rows {
		rows[i].Embedding = vectors[rows[i].ContentHash]
		if missingSet[rows[i].ContentHash] {
			embedded++
		} else {
			reused++
		}
	}
	if err := p.store.InsertChunks(ctx, snap, rows); err != nil {
		return Result{}, fmt.Errorf("insert chunks: %w", err)
	}
	return Result{Files: len(files), Chunks: len(rows), Embedded: embedded, Reused: reused}, nil
}

// chunkAll parses files on a pool of ChunkWorkers goroutines. Results are
// stored by file index so output order is deterministic.
func (p *Pipeline) chunkAll(ctx context.Context, files []walker.File) ([]chunker.Chunk, error) {
	perFile := make([][]chunker.Chunk, len(files))
	g, ctx := errgroup.WithContext(ctx)
	jobs := make(chan int)
	g.Go(func() error {
		defer close(jobs)
		for i := range files {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	for range p.opts.ChunkWorkers {
		g.Go(func() error {
			for i := range jobs {
				perFile[i] = p.chunker.Chunk(files[i])
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	var all []chunker.Chunk
	for _, cs := range perFile {
		all = append(all, cs...)
	}
	return all, nil
}

// embedAll embeds the missing hashes in batches on EmbedConcurrency
// goroutines and writes results into vectors. The first error cancels the
// rest.
func (p *Pipeline) embedAll(ctx context.Context, missing []string, texts map[string]string, vectors map[string][]float32) error {
	var mu sync.Mutex
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(p.opts.EmbedConcurrency)
	for start := 0; start < len(missing); start += p.opts.EmbedBatchSize {
		batch := missing[start:min(start+p.opts.EmbedBatchSize, len(missing))]
		g.Go(func() error {
			inputs := make([]string, len(batch))
			for i, h := range batch {
				inputs[i] = texts[h]
			}
			vecs, err := p.embedder.Embed(ctx, inputs, embed.Document)
			if err != nil {
				return fmt.Errorf("embed batch: %w", err)
			}
			if len(vecs) != len(batch) {
				return fmt.Errorf("embed batch: got %d vectors for %d inputs", len(vecs), len(batch))
			}
			mu.Lock()
			for i, h := range batch {
				vectors[h] = vecs[i]
			}
			mu.Unlock()
			return nil
		})
	}
	return g.Wait()
}

func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
