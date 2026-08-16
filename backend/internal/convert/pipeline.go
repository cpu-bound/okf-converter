package convert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"okf-converter/backend/internal/bundle"
	"okf-converter/backend/internal/metrics"
	"okf-converter/backend/internal/storage"
)

// Status values the pipeline moves a file through, beyond the upload-side
// 'pending'/'ready' handled by internal/files.
const (
	StatusConverting = "converting"
	StatusConverted  = "converted"
	StatusFailed     = "failed"
)

// markdownContentType is what the bundle's files are stored as, so a browser
// following a presigned URL renders or downloads them sensibly rather than
// getting application/octet-stream.
const markdownContentType = "text/markdown; charset=utf-8"

// FileStatusUpdater is satisfied by *files.PgFileRepository. Declared here
// (rather than importing internal/files) so this package depends on files
// only through the narrow slice of behavior it actually needs.
type FileStatusUpdater interface {
	UpdateStatus(ctx context.Context, id, status string) error
	// SaveValidation records the verdict of the bundle's validation and the
	// full rule-by-rule report, so the API can explain to the user why a
	// bundle was published, published with warnings, or refused.
	SaveValidation(ctx context.Context, id, verdict string, report []byte) error
	// ClearValidation drops the verdict of a previous attempt, so a retry
	// never shows a stale result while it is running.
	ClearValidation(ctx context.Context, id string) error
}

// OutputRecorder is satisfied by *files.PgOutputRepository, for the same
// reason as FileStatusUpdater. One row per file in the bundle, in reading
// order.
type OutputRecorder interface {
	Create(ctx context.Context, fileID, objectKey, name string, position int, size int64) error
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

// BundleConverter turns an uploaded document into an OKF bundle: it
// downloads the source object, splits it into logical units, builds
// index.md, log.md and one Markdown document per unit (see
// internal/bundle), stores each of them as its own object, and writes the
// whole bundle as a single downloadable archive at a deterministic key (see
// storage.ResultObjectName).
//
// The unit of success is the bundle, never an individual file: a run either
// produces every required file or fails and marks the job failed.
type BundleConverter struct {
	storage storage.Storage
	files   FileStatusUpdater
	outputs OutputRecorder
	jobs    JobStatusUpdater

	// validator classifies the built bundle before it is published. It is a
	// field rather than a direct call to bundle.Validate so tests can drive
	// the refusal path - a correct generator never produces an invalid bundle
	// on its own, and the branch that refuses to publish one is exactly the
	// branch that must not rot.
	validator func(bundle.Bundle) bundle.Report
}

func NewBundleConverter(store storage.Storage, files FileStatusUpdater, outputs OutputRecorder, jobs JobStatusUpdater) *BundleConverter {
	return &BundleConverter{
		storage:   store,
		files:     files,
		outputs:   outputs,
		jobs:      jobs,
		validator: bundle.Validate,
	}
}

func (c *BundleConverter) Convert(ctx context.Context, job Job) error {
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

func (c *BundleConverter) convert(ctx context.Context, job Job) error {
	// The log is threaded through the whole run and ends up as the bundle's
	// log.md, so it records what actually happened rather than a summary
	// written afterwards.
	log := bundle.NewLog()
	log.Step("trabajo %s recibido para el archivo %s", job.JobID, job.FileID)

	// Clear any output rows a previous attempt for this file left behind, so
	// a retry starts from a clean slate rather than colliding with (or
	// leaving stale) rows from the attempt it's replacing. The bundle and
	// archive object keys themselves are deterministic from job.ObjectKey,
	// so re-running this simply overwrites them in object storage - no
	// cleanup needed there.
	if err := c.outputs.ClearForFile(ctx, job.FileID); err != nil {
		return fmt.Errorf("clear previous outputs: %w", err)
	}
	if err := c.files.ClearValidation(ctx, job.FileID); err != nil {
		return fmt.Errorf("clear previous validation: %w", err)
	}

	src, err := c.storage.GetObject(ctx, job.ObjectKey)
	if err != nil {
		return fmt.Errorf("download source: %w", err)
	}
	defer src.Close()

	log.Step("documento original descargado desde el almacenamiento de objetos (`%s`)", job.ObjectKey)

	units, err := extractUnits(job.ContentType, job.OriginalName, src, log)
	if err != nil {
		return fmt.Errorf("extract units: %w", err)
	}

	source := bundle.Source{
		OriginalName: job.OriginalName,
		ContentType:  job.ContentType,
		Size:         job.Size,
		FileID:       job.FileID,
		JobID:        job.JobID,
		ConvertedAt:  time.Now().UTC(),
	}

	b, err := bundle.Build(source, units, log)
	if err != nil {
		return fmt.Errorf("build bundle: %w", err)
	}

	log.Step("bundle armado: %d archivo(s), raíz `%s/`", len(b.Files), b.Root)

	if err := c.validate(ctx, job, &b, log); err != nil {
		return err
	}

	if err := c.store(ctx, job, b); err != nil {
		return err
	}

	return nil
}

// validate is the gate the enunciado (§6) puts in front of publication: the
// bundle is checked while it is still only in memory, so an invalid one never
// reaches object storage and no download is ever offered for it.
//
// The verdict is persisted either way - a refused bundle is exactly the case
// where the user most needs to be told which rule failed.
func (c *BundleConverter) validate(ctx context.Context, job Job, b *bundle.Bundle, log *bundle.Log) error {
	report := c.validator(*b)

	log.Step("bundle validado: %s (plataforma: %s, conformidad OKF: %s)",
		report.Verdict.Label(), report.Platform.Label(), report.OKF.Label())
	for _, check := range report.Failures() {
		log.Step("regla no superada — %s: %s", check.Rule, strings.Join(check.Details, "; "))
	}

	// Attaching the report re-renders log.md, so the published bundle carries
	// its own validation record.
	b.SetValidation(report)
	metrics.BundlesValidatedTotal.WithLabelValues(string(report.Verdict)).Inc()

	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode validation report: %w", err)
	}
	if err := c.files.SaveValidation(ctx, job.FileID, string(report.Verdict), payload); err != nil {
		return fmt.Errorf("save validation report: %w", err)
	}

	if !report.Verdict.Publishable() {
		return fmt.Errorf("el bundle generado no superó la validación — %s", report.Summary())
	}

	return nil
}

// store writes every file of the bundle as its own object (so a client can
// read index.md without downloading the whole thing) and then the packaged
// archive at the deterministic result key.
func (c *BundleConverter) store(ctx context.Context, job Job, b bundle.Bundle) error {
	prefix := storage.BundleObjectPrefix(job.ObjectKey)

	for position, f := range b.OrderedFiles() {
		key := prefix + "/" + f.Name

		if err := c.storage.PutObject(ctx, key, bytes.NewReader(f.Content), int64(len(f.Content)), markdownContentType); err != nil {
			return fmt.Errorf("store bundle file %q: %w", f.Name, err)
		}

		if err := c.outputs.Create(ctx, job.FileID, key, f.Name, position, int64(len(f.Content))); err != nil {
			return fmt.Errorf("record bundle file %q: %w", f.Name, err)
		}
	}

	archive, err := b.Zip()
	if err != nil {
		return fmt.Errorf("package bundle: %w", err)
	}

	resultKey := storage.ResultObjectName(job.ObjectKey)
	if err := c.storage.PutObject(ctx, resultKey, bytes.NewReader(archive), int64(len(archive)), "application/zip"); err != nil {
		return fmt.Errorf("store bundle archive: %w", err)
	}

	return nil
}
