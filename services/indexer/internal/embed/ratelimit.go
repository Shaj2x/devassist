package embed

import (
	"context"

	"golang.org/x/time/rate"
)

// RateLimited shares one token bucket across every goroutine that embeds,
// so N concurrent workers together stay under the provider's request limit.
type RateLimited struct {
	Embedder
	limiter *rate.Limiter
}

// WithRateLimit allows rps requests per second with a burst of burst.
func WithRateLimit(e Embedder, rps float64, burst int) *RateLimited {
	return &RateLimited{Embedder: e, limiter: rate.NewLimiter(rate.Limit(rps), max(burst, 1))}
}

func (r *RateLimited) Embed(ctx context.Context, texts []string, t InputType) ([][]float32, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	return r.Embedder.Embed(ctx, texts, t)
}
