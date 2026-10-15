package pipeline

import (
	"errors"

	"github.com/Shaj2x/devassist/libs/gocommon/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	indexRuns = metrics.Factory().NewCounterVec(prometheus.CounterOpts{
		Name: "indexer_runs_total",
		Help: "Index runs by outcome: indexed, skipped (commit already indexed), busy (locked), failed.",
	}, []string{"outcome"})
	indexDuration = metrics.Factory().NewHistogram(prometheus.HistogramOpts{
		Name:    "indexer_run_duration_seconds",
		Help:    "Wall time of index runs that built a snapshot (clone, chunk, embed, store).",
		Buckets: []float64{.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800},
	})
	indexChunks = metrics.Factory().NewCounterVec(prometheus.CounterOpts{
		Name: "indexer_chunks_total",
		Help: "Chunks stored, by whether the embedding was computed (embedded) or reused from an earlier commit (reused).",
	}, []string{"source"})
)

func observe(res Result, err error) {
	switch {
	case errors.Is(err, ErrBusy):
		indexRuns.WithLabelValues("busy").Inc()
		return
	case err != nil:
		indexRuns.WithLabelValues("failed").Inc()
	case res.Skipped:
		indexRuns.WithLabelValues("skipped").Inc()
		return
	default:
		indexRuns.WithLabelValues("indexed").Inc()
		indexChunks.WithLabelValues("embedded").Add(float64(res.Embedded))
		indexChunks.WithLabelValues("reused").Add(float64(res.Reused))
	}
	if res.Duration > 0 {
		indexDuration.Observe(res.Duration.Seconds())
	}
}
