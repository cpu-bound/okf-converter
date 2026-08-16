package files

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("file not found")

// File is the shape returned to the frontend. Field names are snake_case
// to match the existing API contract, not idiomatic Go/JSON casing.
type File struct {
	ID           string `json:"id"`
	OriginalName string `json:"original_name"`
	ContentType  string `json:"content_type"`
	Size         int64  `json:"size"`
	Status       string `json:"status"`
	// Validation classifies the bundle built for this file - valid,
	// valid_with_warnings or invalid. Null until a conversion has produced a
	// bundle to classify.
	Validation *string `json:"validation,omitempty"`
}

// StatusConverted is the point at which a file's bundle has been validated
// and written to object storage. It is the only state in which a bundle may
// be downloaded (§6): an invalid bundle is never stored, and its file stays
// 'failed'.
const StatusConverted = "converted"

// Published reports whether this file's bundle passed validation and was
// actually stored, which is what gates the download.
func (rec FileRecord) Published() bool { return rec.Status == StatusConverted }

// FileRecord carries the fields the confirm/retry/outputs flows need
// internally (object_key, declared size) alongside the public File fields.
type FileRecord struct {
	ID           string
	ObjectKey    string
	OriginalName string
	ContentType  string
	Size         int64
	Status       string
	Validation   *string
	// ValidationReport is bundle.Report as stored, passed through to the
	// client verbatim rather than decoded and re-encoded - this package has
	// no reason to know the report's shape.
	ValidationReport []byte
}

// File returns the public shape of rec, for handlers (like Status) that
// only need the fields exposed to the frontend.
func (rec FileRecord) File() File {
	return File{
		ID:           rec.ID,
		OriginalName: rec.OriginalName,
		ContentType:  rec.ContentType,
		Size:         rec.Size,
		Status:       rec.Status,
		Validation:   rec.Validation,
	}
}

type FileRepository interface {
	Create(ctx context.Context, userID, objectKey, originalName, contentType string, size int64) (File, error)
	FindForUser(ctx context.Context, id, userID string) (FileRecord, error)
	MarkReady(ctx context.Context, id string) (File, error)
	// MarkRetrying atomically transitions a file from 'failed' to
	// 'converting', so concurrent/duplicate retry requests only let one
	// caller actually claim the retry. It always returns the file's
	// current state; won reports whether this call made the transition.
	MarkRetrying(ctx context.Context, id string) (file File, won bool, err error)
	Delete(ctx context.Context, id string) error
}

type PgFileRepository struct {
	pool *pgxpool.Pool
}

func NewPgFileRepository(pool *pgxpool.Pool) *PgFileRepository {
	return &PgFileRepository{pool: pool}
}

func (r *PgFileRepository) Create(ctx context.Context, userID, objectKey, originalName, contentType string, size int64) (File, error) {
	var f File

	err := r.pool.QueryRow(ctx,
		`
		INSERT INTO files (user_id, object_key, original_name, content_type, size)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, original_name, content_type, size, status, validation
		`,
		userID, objectKey, originalName, contentType, size,
	).Scan(&f.ID, &f.OriginalName, &f.ContentType, &f.Size, &f.Status, &f.Validation)
	if err != nil {
		return File{}, fmt.Errorf("create file: %w", err)
	}

	return f, nil
}

func (r *PgFileRepository) FindForUser(ctx context.Context, id, userID string) (FileRecord, error) {
	var rec FileRecord

	err := r.pool.QueryRow(ctx,
		`
		SELECT id, object_key, original_name, content_type, size, status, validation, validation_report
		FROM files WHERE id = $1 AND user_id = $2
		`,
		id, userID,
	).Scan(&rec.ID, &rec.ObjectKey, &rec.OriginalName, &rec.ContentType, &rec.Size, &rec.Status,
		&rec.Validation, &rec.ValidationReport)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return FileRecord{}, ErrNotFound
		}
		return FileRecord{}, fmt.Errorf("find file: %w", err)
	}

	return rec, nil
}

// MarkRetrying is the CAS guard behind Handlers.Retry: only a request that
// finds the file still in 'failed' actually transitions it (and is told
// won=true), so a duplicate/concurrent retry call is always safe to make -
// it just observes the state the winning call already produced.
func (r *PgFileRepository) MarkRetrying(ctx context.Context, id string) (File, bool, error) {
	var f File

	err := r.pool.QueryRow(ctx,
		`
		UPDATE files SET status = 'converting'
		WHERE id = $1 AND status = 'failed'
		RETURNING id, original_name, content_type, size, status, validation
		`,
		id,
	).Scan(&f.ID, &f.OriginalName, &f.ContentType, &f.Size, &f.Status, &f.Validation)
	if err == nil {
		return f, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return File{}, false, fmt.Errorf("mark retrying: %w", err)
	}

	err = r.pool.QueryRow(ctx,
		`SELECT id, original_name, content_type, size, status, validation FROM files WHERE id = $1`,
		id,
	).Scan(&f.ID, &f.OriginalName, &f.ContentType, &f.Size, &f.Status, &f.Validation)
	if err != nil {
		return File{}, false, fmt.Errorf("find file: %w", err)
	}

	return f, false, nil
}

func (r *PgFileRepository) MarkReady(ctx context.Context, id string) (File, error) {
	var f File

	err := r.pool.QueryRow(ctx,
		`
		UPDATE files SET status = 'ready' WHERE id = $1
		RETURNING id, original_name, content_type, size, status, validation
		`,
		id,
	).Scan(&f.ID, &f.OriginalName, &f.ContentType, &f.Size, &f.Status, &f.Validation)
	if err != nil {
		return File{}, fmt.Errorf("mark file ready: %w", err)
	}

	return f, nil
}

// UpdateStatus is used by the conversion pipeline to move a file through
// 'converting' -> 'converted'/'failed', separately from MarkReady which
// covers the upload-confirmation step.
func (r *PgFileRepository) UpdateStatus(ctx context.Context, id, status string) error {
	if _, err := r.pool.Exec(ctx, `UPDATE files SET status = $2 WHERE id = $1`, id, status); err != nil {
		return fmt.Errorf("update file status: %w", err)
	}
	return nil
}

// SaveValidation records how the bundle built for this file was classified,
// together with the full report. Called by the conversion pipeline before it
// decides whether to publish, so the verdict is on record even for a bundle
// that is refused.
func (r *PgFileRepository) SaveValidation(ctx context.Context, id, verdict string, report []byte) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE files SET validation = $2, validation_report = $3 WHERE id = $1`,
		id, verdict, report,
	)
	if err != nil {
		return fmt.Errorf("save validation: %w", err)
	}
	return nil
}

// ClearValidation drops a previous attempt's verdict when a new conversion
// starts, so a retry in flight never shows the result it is replacing.
func (r *PgFileRepository) ClearValidation(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE files SET validation = NULL, validation_report = NULL WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("clear validation: %w", err)
	}
	return nil
}

func (r *PgFileRepository) Delete(ctx context.Context, id string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM files WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete file: %w", err)
	}
	return nil
}
