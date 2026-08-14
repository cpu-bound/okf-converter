package convert

import (
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
}

// SplitConverter downloads a job's source object, extracts its text content,
// splits it into paragraph-sized chunks (see extract.go), and stores each
// chunk as its own .txt object - one source document in, many .txt files
// out.
type SplitConverter struct {
	storage storage.Storage
	files   FileStatusUpdater
	outputs OutputRecorder
}

func NewSplitConverter(store storage.Storage, files FileStatusUpdater, outputs OutputRecorder) *SplitConverter {
	return &SplitConverter{storage: store, files: files, outputs: outputs}
}

func (c *SplitConverter) Convert(ctx context.Context, job Job) error {
	if err := c.files.UpdateStatus(ctx, job.FileID, StatusConverting); err != nil {
		return fmt.Errorf("mark converting: %w", err)
	}

	if err := c.convert(ctx, job); err != nil {
		if statusErr := c.files.UpdateStatus(ctx, job.FileID, StatusFailed); statusErr != nil {
			return fmt.Errorf("%w (and mark failed: %v)", err, statusErr)
		}
		return err
	}

	if err := c.files.UpdateStatus(ctx, job.FileID, StatusConverted); err != nil {
		return fmt.Errorf("mark converted: %w", err)
	}
	return nil
}

func (c *SplitConverter) convert(ctx context.Context, job Job) error {
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

	return nil
}

// chunkBaseKey derives a per-file prefix for chunk objects from the source
// object key (<userID>/<uuid>.<ext>), e.g. "<userID>/<uuid>-chunks".
func chunkBaseKey(objectKey string) string {
	if idx := strings.LastIndex(objectKey, "."); idx != -1 {
		return objectKey[:idx] + "-chunks"
	}
	return objectKey + "-chunks"
}
