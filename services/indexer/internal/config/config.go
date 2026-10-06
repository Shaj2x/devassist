// Package config holds the indexer's settings, read from the environment.
package config

import (
	"fmt"
	"runtime"
	"time"

	base "github.com/Shaj2x/devassist/libs/gocommon/config"
)

// Dimensions is fixed by the code_chunks.embedding column (vector(1024)).
const Dimensions = 1024

type Config struct {
	base.Base

	// Embeddings
	EmbeddingProvider    string // local | openai | voyage
	EmbeddingModel       string
	EmbeddingBatchSize   int
	EmbeddingConcurrency int
	EmbeddingRPS         float64 // requests per second across all workers
	EmbeddingTimeout     time.Duration
	EmbeddingMaxRetries  int
	OpenAIAPIKey         string
	OpenAIBaseURL        string
	VoyageAPIKey         string
	VoyageBaseURL        string

	// Indexing
	WorkDir      string // where repositories are cloned (temporary)
	ChunkWorkers int
	MaxFileBytes int
	LockTTL      time.Duration
	GitHubToken  string // optional, for private GitHub repositories

	// Search
	SearchCacheTTL time.Duration
}

func Load() (Config, error) {
	b, err := base.LoadBase(8080)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Base:              b,
		EmbeddingProvider: base.String("EMBEDDING_PROVIDER", "local"),
		EmbeddingModel:    base.String("EMBEDDING_MODEL", ""),
		OpenAIAPIKey:      base.String("OPENAI_API_KEY", ""),
		OpenAIBaseURL:     base.String("OPENAI_BASE_URL", "https://api.openai.com/v1"),
		VoyageAPIKey:      base.String("VOYAGE_API_KEY", ""),
		VoyageBaseURL:     base.String("VOYAGE_BASE_URL", "https://api.voyageai.com/v1"),
		WorkDir:           base.String("INDEXER_WORK_DIR", "/tmp/devassist-index"),
		GitHubToken:       base.String("GITHUB_TOKEN", ""),
	}

	ints := []struct {
		dst  *int
		key  string
		def  int
		min_ int
	}{
		{&cfg.EmbeddingBatchSize, "EMBEDDING_BATCH_SIZE", 64, 1},
		{&cfg.EmbeddingConcurrency, "EMBEDDING_CONCURRENCY", 4, 1},
		{&cfg.EmbeddingMaxRetries, "EMBEDDING_MAX_RETRIES", 4, 0},
		{&cfg.ChunkWorkers, "INDEXER_CHUNK_WORKERS", runtime.NumCPU(), 1},
		{&cfg.MaxFileBytes, "INDEXER_MAX_FILE_BYTES", 256 << 10, 1},
	}
	for _, f := range ints {
		v, err := base.Int(f.key, f.def)
		if err != nil {
			return Config{}, err
		}
		if v < f.min_ {
			return Config{}, fmt.Errorf("%s must be >= %d", f.key, f.min_)
		}
		*f.dst = v
	}

	durations := []struct {
		dst *time.Duration
		key string
		def time.Duration
	}{
		{&cfg.EmbeddingTimeout, "EMBEDDING_TIMEOUT", 60 * time.Second},
		{&cfg.LockTTL, "INDEX_LOCK_TTL", 2 * time.Minute},
		{&cfg.SearchCacheTTL, "SEARCH_CACHE_TTL", 10 * time.Minute},
	}
	for _, f := range durations {
		v, err := base.Duration(f.key, f.def)
		if err != nil {
			return Config{}, err
		}
		*f.dst = v
	}

	rps, err := base.Int("EMBEDDING_REQUESTS_PER_MINUTE", 300)
	if err != nil {
		return Config{}, err
	}
	cfg.EmbeddingRPS = float64(rps) / 60

	dims, err := base.Int("EMBEDDING_DIMENSIONS", Dimensions)
	if err != nil {
		return Config{}, err
	}
	if dims != Dimensions {
		return Config{}, fmt.Errorf("EMBEDDING_DIMENSIONS=%d but the database column is vector(%d)", dims, Dimensions)
	}
	return cfg, nil
}
