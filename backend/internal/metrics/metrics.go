// Package metrics holds the Prometheus collectors for the conversion job
// pipeline, exposed on GET /metrics (wired in cmd/api/main.go).
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// JobsProcessedTotal counts conversion jobs by outcome ("success" or
	// "failed"), incremented once per job in RabbitQueue.process.
	JobsProcessedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "convert_jobs_processed_total",
		Help: "Conversion jobs processed, by outcome.",
	}, []string{"status"})

	// JobDurationSeconds observes wall-clock time spent in Converter.Convert
	// for a single job, regardless of outcome.
	JobDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "convert_job_duration_seconds",
		Help:    "Time spent converting a single job.",
		Buckets: prometheus.DefBuckets,
	})
)
