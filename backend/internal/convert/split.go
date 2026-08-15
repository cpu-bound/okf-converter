package convert

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"

	"okf-converter/backend/internal/storage"
)

// Status values the pipeline moves a file through, beyond the upload-side
// 'pending'/'ready' handled by internal/files.
const (
	StatusConverting = "converting"
	StatusConverted  = "converted"
	StatusFailed     = "failed"
)

// FileStatusUpdater is satisfied by *files.PgFileRepository. Declared here
// (rather than importing internal/files) so this package depends on files
// only through the narrow slice of behavior it actually needs.
type FileStatusUpdater interface {
	UpdateStatus(ctx context.Context, id, status string) error
}

// OutputRecorder is satisfied by *files.PgOutputRepository, for the same
// reason as FileStatusUpdater.
type OutputRecorder interface {
	Create(ctx context.Context, fileID, objectKey string, chunkIndex int, size int64) error
	// ClearForFile deletes any output rows left over from a previous
	// attempt, so a retry never collides with (or leaves stale) rows from
	// the attempt it's replacing.
	ClearForFile(ctx context.Context, fileID string) error
}

// JobStatusUpdater is satisfied by *files.PgJobRepository, for the same
// reason as FileStatusUpdater - it tracks the individual conversion
// attempt (job.JobID), distinct from the file's overall status.
type JobStatusUpdater interface {
	UpdateStatus(ctx context.Context, jobID, status string, errMsg *string) error
}

// SplitConverter downloads a job's source object, extracts its text content,
// splits it into paragraph-sized chunks (see extract.go), stores each chunk
// as its own .txt object, and bundles all chunks into a single result zip
// at a deterministic key (see storage.ResultObjectName) - one source
// document in, many .txt chunk files plus one zip out.
type SplitConverter struct {
	storage storage.Storage
	files   FileStatusUpdater
	outputs OutputRecorder
	jobs    JobStatusUpdater
}

func NewSplitConverter(store storage.Storage, files FileStatusUpdater, outputs OutputRecorder, jobs JobStatusUpdater) *SplitConverter {
	return &SplitConverter{storage: store, files: files, outputs: outputs, jobs: jobs}
}

func (c *SplitConverter) Convert(ctx context.Context, job Job) error {
	if err := c.files.UpdateStatus(ctx, job.FileID, StatusConverting); err != nil {
		return fmt.Errorf("mark converting: %w", err)
	}
	if err := c.jobs.UpdateStatus(ctx, job.JobID, StatusConverting, nil); err != nil {
		return fmt.Errorf("mark job converting: %w", err)
	}

	if err := c.convert(ctx, job); err != nil {
		errMsg := err.Error()
		if statusErr := c.files.UpdateStatus(ctx, job.FileID, StatusFailed); statusErr != nil {
			return fmt.Errorf("%w (and mark failed: %v)", err, statusErr)
		}
		if jobErr := c.jobs.UpdateStatus(ctx, job.JobID, StatusFailed, &errMsg); jobErr != nil {
			return fmt.Errorf("%w (and mark job failed: %v)", err, jobErr)
		}
		return err
	}

	if err := c.files.UpdateStatus(ctx, job.FileID, StatusConverted); err != nil {
		return fmt.Errorf("mark converted: %w", err)
	}
	if err := c.jobs.UpdateStatus(ctx, job.JobID, StatusConverted, nil); err != nil {
		return fmt.Errorf("mark job converted: %w", err)
	}
	return nil
}

func (c *SplitConverter) convert(ctx context.Context, job Job) error {
	// Clear any output rows a previous attempt for this file left behind,
	// so a retry starts from a clean slate rather than colliding with (or
	// leaving stale) rows from the attempt it's replacing. The chunk and
	// zip object keys themselves are deterministic from job.ObjectKey, so
	// re-running this simply overwrites them in MinIO - no cleanup needed
	// there.
	if err := c.outputs.ClearForFile(ctx, job.FileID); err != nil {
		return fmt.Errorf("clear previous outputs: %w", err)
	}

	src, err := c.storage.GetObject(ctx, job.ObjectKey)
	if err != nil {
		return fmt.Errorf("download source: %w", err)
	}
	defer src.Close()

	paragraphs, err := extractParagraphs(job.ContentType, job.OriginalName, src)
	if err != nil {
		return fmt.Errorf("extract text: %w", err)
	}

	if len(paragraphs) == 0 {
		return fmt.Errorf("no extractable text content")
	}

	base := chunkBaseKey(job.ObjectKey)

	for i, paragraph := range paragraphs {
		chunkKey := fmt.Sprintf("%s/chunk-%04d.txt", base, i)
		content := []byte(paragraph)

		if err := c.storage.PutObject(ctx, chunkKey, bytes.NewReader(content), int64(len(content)), "text/plain; charset=utf-8"); err != nil {
			return fmt.Errorf("store chunk %d: %w", i, err)
		}

		if err := c.outputs.Create(ctx, job.FileID, chunkKey, i, int64(len(content))); err != nil {
			return fmt.Errorf("record chunk %d: %w", i, err)
		}
	}

	resultBytes, err := buildResultZip(paragraphs)
	if err != nil {
		return fmt.Errorf("build result zip: %w", err)
	}

	resultKey := storage.ResultObjectName(job.ObjectKey)
	if err := c.storage.PutObject(ctx, resultKey, bytes.NewReader(resultBytes), int64(len(resultBytes)), "application/zip"); err != nil {
		return fmt.Errorf("store result zip: %w", err)
	}

	return nil
}

// buildResultZip bundles every paragraph as its own chunk-%04d.txt entry -
// matching the individual chunk objects' naming - into a single in-memory
// zip archive, for a client to download as one deterministic result.
func buildResultZip(paragraphs []string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for i, paragraph := range paragraphs {
		entry, err := zw.Create(fmt.Sprintf("chunk-%04d.txt", i))
		if err != nil {
			return nil, fmt.Errorf("create zip entry %d: %w", i, err)
		}
		if _, err := entry.Write([]byte(paragraph)); err != nil {
			return nil, fmt.Errorf("write zip entry %d: %w", i, err)
		}
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("close zip writer: %w", err)
	}

	return buf.Bytes(), nil
}

// chunkBaseKey derives a per-file prefix for chunk objects from the source
// object key (<userID>/<uuid>.<ext>), e.g. "<userID>/<uuid>-chunks".
func chunkBaseKey(objectKey string) string {
	if idx := strings.LastIndex(objectKey, "."); idx != -1 {
		return objectKey[:idx] + "-chunks"
	}
	return objectKey + "-chunks"
}
