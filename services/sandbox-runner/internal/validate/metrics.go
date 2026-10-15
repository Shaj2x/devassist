package validate

import (
	"time"

	"github.com/Shaj2x/devassist/libs/gocommon/events"
	"github.com/Shaj2x/devassist/libs/gocommon/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	validations = metrics.Factory().NewCounterVec(prometheus.CounterOpts{
		Name: "sandbox_validations_total",
		Help: "Patch validations by overall status: passed, failed, error, timeout.",
	}, []string{"status"})
	validationDuration = metrics.Factory().NewHistogramVec(prometheus.HistogramOpts{
		Name:    "sandbox_validation_duration_seconds",
		Help:    "End-to-end validation time, including waiting for a sandbox slot.",
		Buckets: []float64{1, 2.5, 5, 10, 20, 30, 60, 120, 300, 600},
	}, []string{"status"})
	checks = metrics.Factory().NewCounterVec(prometheus.CounterOpts{
		Name: "sandbox_checks_total",
		Help: "Individual check results: check is tests, security or static_analysis.",
	}, []string{"check", "status"})
	active = metrics.Factory().NewGauge(prometheus.GaugeOpts{
		Name: "sandbox_active",
		Help: "Sandboxes running right now (bounded by SANDBOX_CONCURRENCY).",
	})
	slotWait = metrics.Factory().NewHistogram(prometheus.HistogramOpts{
		Name:    "sandbox_slot_wait_seconds",
		Help:    "Time a validation waited for a free sandbox slot. Sustained waits mean scale out.",
		Buckets: []float64{.01, .1, .5, 1, 5, 15, 30, 60, 120, 300},
	})
)

func observe(res events.ValidationCompletedPayload, took time.Duration) {
	validations.WithLabelValues(res.Status).Inc()
	validationDuration.WithLabelValues(res.Status).Observe(took.Seconds())
	checks.WithLabelValues("tests", res.Tests.Status).Inc()
	checks.WithLabelValues("security", res.Security.Status).Inc()
	checks.WithLabelValues("static_analysis", res.StaticAnalysis.Status).Inc()
}
