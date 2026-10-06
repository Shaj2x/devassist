// Package embed turns text into vectors.
//
// Three providers implement Embedder: a local hashing embedder (no API key,
// deterministic, lexical rather than semantic), OpenAI, and Voyage. The
// pipeline only sees the interface, wrapped in a rate limiter.
package embed

import (
	"context"
	"fmt"
	"math"
)

// InputType tells providers that distinguish them whether text is a stored
// document or a search query (Voyage optimizes each differently).
type InputType string

const (
	Document InputType = "document"
	Query    InputType = "query"
)

// Embedder embeds a batch of texts. Implementations must return exactly one
// vector of Dimensions() length per input, in order.
type Embedder interface {
	Embed(ctx context.Context, texts []string, inputType InputType) ([][]float32, error)
	// Model identifies the embedding space, e.g. "openai/text-embedding-3-small".
	// Embeddings are only comparable (and reusable) within one model.
	Model() string
	Dimensions() int
}

// Options configures New.
type Options struct {
	Provider      string // local | openai | voyage
	Model         string // provider default when empty
	Dimensions    int
	OpenAIAPIKey  string
	OpenAIBaseURL string
	VoyageAPIKey  string
	VoyageBaseURL string
	HTTP          HTTPOptions
}

// New builds the configured provider.
func New(o Options) (Embedder, error) {
	switch o.Provider {
	case "", "local":
		return NewLocal(o.Dimensions), nil
	case "openai":
		if o.OpenAIAPIKey == "" {
			return nil, fmt.Errorf("EMBEDDING_PROVIDER=openai requires OPENAI_API_KEY")
		}
		return NewOpenAI(o.OpenAIBaseURL, o.OpenAIAPIKey, orDefault(o.Model, "text-embedding-3-small"), o.Dimensions, o.HTTP), nil
	case "voyage":
		if o.VoyageAPIKey == "" {
			return nil, fmt.Errorf("EMBEDDING_PROVIDER=voyage requires VOYAGE_API_KEY")
		}
		return NewVoyage(o.VoyageBaseURL, o.VoyageAPIKey, orDefault(o.Model, "voyage-code-3"), o.Dimensions, o.HTTP), nil
	default:
		return nil, fmt.Errorf("unknown EMBEDDING_PROVIDER %q (want local, openai or voyage)", o.Provider)
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// normalize scales v to unit length in place, so cosine distance and dot
// product agree across providers.
func normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}

func checkShape(vectors [][]float32, n, dims int) error {
	if len(vectors) != n {
		return fmt.Errorf("provider returned %d vectors for %d inputs", len(vectors), n)
	}
	for i, v := range vectors {
		if len(v) != dims {
			return fmt.Errorf("vector %d has %d dimensions, want %d", i, len(v), dims)
		}
	}
	return nil
}
