// Package metrics exposes Prometheus metrics for the Go services.
//
// Everything is registered on one Registry per process (not the global
// default), so tests can inspect it and services add their own collectors
// with promauto.With(metrics.Registry). Label values are always bounded:
// HTTP routes use the ServeMux pattern ("POST /v1/search"), never the raw
// path, so ids in URLs cannot blow up cardinality.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds every DevAssist metric in this process.
var Registry = prometheus.NewRegistry()

var factory = promauto.With(Registry)

func init() {
	Registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// Factory registers new collectors on Registry.
func Factory() promauto.Factory { return factory }

var (
	httpRequests = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests by route pattern, method and status code.",
	}, []string{"route", "method", "status"})
	httpDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency by route pattern.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 300},
	}, []string{"route", "method"})

	kafkaMessages = factory.NewCounterVec(prometheus.CounterOpts{
		Name: "kafka_messages_total",
		Help: "Consumed Kafka messages by topic and outcome (processed, retried, dead_lettered).",
	}, []string{"topic", "outcome"})
	kafkaHandler = factory.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kafka_handler_duration_seconds",
		Help:    "Time to handle one message, retries included.",
		Buckets: []float64{.01, .05, .1, .5, 1, 5, 15, 30, 60, 120, 300, 900},
	}, []string{"topic"})
)

// Handler serves the registry in the Prometheus text format.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry})
}

// Instrument wraps a mux so every request is counted and timed by the
// pattern it matched. Unmatched requests are labelled "unmatched".
func Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		if route == "GET /metrics" {
			return // scrapes are not traffic
		}
		httpRequests.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
		httpDuration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush keeps streaming responses working through the wrapper.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// KafkaOutcome records what happened to one consumed message.
func KafkaOutcome(topic, outcome string) {
	kafkaMessages.WithLabelValues(topic, outcome).Inc()
}

// KafkaHandled records how long a message took to handle.
func KafkaHandled(topic string, d time.Duration) {
	kafkaHandler.WithLabelValues(topic).Observe(d.Seconds())
}
