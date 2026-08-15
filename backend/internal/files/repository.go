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
}

// FileRecord carries the fields the confirm/retry/outputs flows need
// internally (object_key, declared size) alongside the public File fields.
type FileRecord struct {
	ID           string
	ObjectKey    string
	OriginalName string
	ContentType  string
	Size         int64
	Status       string
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
		RETURNING id, original_name, content_type, size, status
		`,
		userID, objectKey, originalName, contentType, size,
	).Scan(&f.ID, &f.OriginalName, &f.ContentType, &f.Size, &f.Status)
	if err != nil {
		return File{}, fmt.Errorf("create file: %w", err)
	}

	return f, nil
}

func (r *PgFileRepository) FindForUser(ctx context.Context, id, userID string) (FileRecord, error) {
	var rec FileRecord

	err := r.pool.QueryRow(ctx,
		`SELECT id, object_key, original_name, content_type, size, status FROM files WHERE id = $1 AND user_id = $2`,
		id, userID,
	).Scan(&rec.ID, &rec.ObjectKey, &rec.OriginalName, &rec.ContentType, &rec.Size, &rec.Status)
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
		RETURNING id, original_name, content_type, size, status
		`,
		id,
	).Scan(&f.ID, &f.OriginalName, &f.ContentType, &f.Size, &f.Status)
	if err == nil {
		return f, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return File{}, false, fmt.Errorf("mark retrying: %w", err)
	}

	err = r.pool.QueryRow(ctx,
		`SELECT id, original_name, content_type, size, status FROM files WHERE id = $1`,
		id,
	).Scan(&f.ID, &f.OriginalName, &f.ContentType, &f.Size, &f.Status)
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
		RETURNING id, original_name, content_type, size, status
		`,
		id,
	).Scan(&f.ID, &f.OriginalName, &f.ContentType, &f.Size, &f.Status)
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

func (r *PgFileRepository) Delete(ctx context.Context, id string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM files WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete file: %w", err)
	}
	return nil
}
