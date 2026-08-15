// Package convert runs the file conversion pipeline: Jobs are published to
// a RabbitMQ queue (see queue.go) and a pool of goroutines consumes them,
// extracting the source document's text (see extract.go) and splitting it
// into paragraph chunks, storing each as its own .txt output (see split.go).
package convert

import "context"

type Job struct {
	// JobID identifies this conversion attempt (see files.JobRepository),
	// distinct from FileID which identifies the uploaded file across all of
	// its attempts.
	JobID        string
	FileID       string
	ObjectKey    string
	ContentType  string
	OriginalName string
}

// Converter performs the conversion for a single job, respecting ctx
// cancellation. See SplitConverter (split.go) for the production
// implementation.
type Converter interface {
	Convert(ctx context.Context, job Job) error
}
