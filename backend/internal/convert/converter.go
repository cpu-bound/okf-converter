// Package convert runs the document conversion pipeline. The API publishes
// Jobs to a RabbitMQ queue (see queue.go, Publisher); worker processes
// consume them (Consumer) and run each through a Converter, which extracts
// the source document's text (extract.go), splits it into logical units
// (units.go) and assembles an OKF bundle from them (pipeline.go, and
// internal/bundle).
package convert

import "context"

// Job is everything a worker needs to convert one document. It is
// self-contained on purpose: the worker is a separate process from the API,
// so anything it needs has to travel in the message or live in the database
// and object storage - never in the API's memory.
type Job struct {
	// JobID identifies this conversion attempt (see files.JobRepository),
	// distinct from FileID which identifies the uploaded file across all of
	// its attempts.
	JobID        string
	FileID       string
	ObjectKey    string
	ContentType  string
	OriginalName string
	Size         int64
}

// Converter performs the conversion for a single job, respecting ctx
// cancellation. See BundleConverter (pipeline.go) for the production
// implementation.
type Converter interface {
	Convert(ctx context.Context, job Job) error
}
