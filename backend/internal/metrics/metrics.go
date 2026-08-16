// Package metrics holds the Prometheus collectors for the conversion job
// pipeline. Both binaries expose them on GET /metrics, but each only ever
// moves the ones for its own side of the queue: the API increments
// JobsEnqueuedTotal, the workers increment everything else. Prometheus
// scrapes them separately (see observability/prometheus/prometheus.yml), so
// the gap between jobs enqueued and jobs processed is visible as a real
// backlog rather than hidden inside one process.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// JobsEnqueuedTotal counts conversion jobs published to the queue,
	// incremented by the API in Publisher.Enqueue.
	JobsEnqueuedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "convert_jobs_enqueued_total",
		Help: "Conversion jobs published to the queue.",
	})

	// JobsProcessedTotal counts conversion jobs by outcome ("success" or
	// "failed"), incremented once per job in Consumer.process.
	JobsProcessedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "convert_jobs_processed_total",
		Help: "Conversion jobs processed, by outcome.",
	}, []string{"status"})

	// JobsInFlight tracks how many jobs a worker process is converting right
	// now. Summed across worker replicas it shows how much of the pool is
	// busy, which is what makes scaling the worker container observable.
	JobsInFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "convert_jobs_in_flight",
		Help: "Conversion jobs currently being processed by this worker.",
	})

	// JobDurationSeconds observes wall-clock time spent in Converter.Convert
	// for a single job, regardless of outcome.
	JobDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "convert_job_duration_seconds",
		Help:    "Time spent converting a single job.",
		Buckets: prometheus.DefBuckets,
	})

	// JobsSkippedTotal counts deliveries dropped because the job was already
	// claimed - duplicated publishes and redeliveries of work that is done.
	// A non-zero value here is idempotency working, not a problem.
	JobsSkippedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "convert_jobs_skipped_total",
		Help: "Queue deliveries dropped because the job was already claimed.",
	})

	// JobsRetriedTotal counts failed attempts scheduled for another try.
	JobsRetriedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "convert_jobs_retried_total",
		Help: "Failed conversion attempts scheduled for an automatic retry.",
	})

	// JobsDeadLetteredTotal counts messages parked in the dead-letter queue,
	// by why they got there ("exhausted" or "malformed").
	JobsDeadLetteredTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "convert_jobs_dead_lettered_total",
		Help: "Messages moved to the dead-letter queue, by reason.",
	}, []string{"reason"})

	// BundlesValidatedTotal counts built bundles by validation verdict
	// ("valid", "valid_with_warnings", "invalid"), so the share of documents
	// that fail validation is visible separately from the share of jobs that
	// crash - two very different failure modes that would otherwise both show
	// up only as a failed job.
	BundlesValidatedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "convert_bundles_validated_total",
		Help: "Bundles validated before publishing, by verdict.",
	}, []string{"verdict"})
)
