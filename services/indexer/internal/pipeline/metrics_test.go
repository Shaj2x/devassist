package pipeline

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObserveRecordsOutcomes(t *testing.T) {
	before := func(o string) float64 { return testutil.ToFloat64(indexRuns.WithLabelValues(o)) }
	indexed, skipped, busy, failed := before("indexed"), before("skipped"), before("busy"), before("failed")
	reused := testutil.ToFloat64(indexChunks.WithLabelValues("reused"))

	observe(Result{Embedded: 3, Reused: 7, Duration: time.Second}, nil)
	observe(Result{Skipped: true}, nil)
	observe(Result{}, ErrBusy)
	observe(Result{Duration: time.Second}, errors.New("clone failed"))

	for outcome, was := range map[string]float64{"indexed": indexed, "skipped": skipped, "busy": busy, "failed": failed} {
		if got := before(outcome); got != was+1 {
			t.Errorf("%s: got %v, want %v", outcome, got, was+1)
		}
	}
	if got := testutil.ToFloat64(indexChunks.WithLabelValues("reused")); got != reused+7 {
		t.Errorf("reused chunks: got %v, want %v", got, reused+7)
	}
}
