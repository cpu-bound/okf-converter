package files

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNoJobs = errors.New("no conversion jobs for file")

// Job is one conversion attempt for a file. RetryOf, when set, points at the
// attempt this one retries, so the full retry history for a file is a
// linked list queryable by FileID.
type Job struct {
	ID      string
	FileID  string
	RetryOf *string
	Status  string
}

type JobRepository interface {
	// Create records a new conversion attempt, optionally linked to the
	// attempt it retries.
	Create(ctx context.Context, fileID string, retryOf *string) (Job, error)
	// UpdateStatus moves a job to converting/converted/failed. errMsg is
	// recorded (and finished_at set) only for a terminal status.
	UpdateStatus(ctx context.Context, jobID, status string, errMsg *string) error
	// LatestForFile returns the most recently created job for fileID.
	LatestForFile(ctx context.Context, fileID string) (Job, error)
}

type PgJobRepository struct {
	pool *pgxpool.Pool
}

func NewPgJobRepository(pool *pgxpool.Pool) *PgJobRepository {
	return &PgJobRepository{pool: pool}
}

func (r *PgJobRepository) Create(ctx context.Context, fileID string, retryOf *string) (Job, error) {
	var j Job
	j.FileID = fileID
	j.RetryOf = retryOf

	err := r.pool.QueryRow(ctx,
		`INSERT INTO conversion_jobs (file_id, retry_of) VALUES ($1, $2) RETURNING id, status`,
		fileID, retryOf,
	).Scan(&j.ID, &j.Status)
	if err != nil {
		return Job{}, fmt.Errorf("create conversion job: %w", err)
	}

	return j, nil
}

func (r *PgJobRepository) UpdateStatus(ctx context.Context, jobID, status string, errMsg *string) error {
	_, err := r.pool.Exec(ctx,
		`
		UPDATE conversion_jobs
		SET status = $2,
		    error = $3,
		    finished_at = CASE WHEN $2 IN ('converted', 'failed') THEN NOW() ELSE finished_at END
		WHERE id = $1
		`,
		jobID, status, errMsg,
	)
	if err != nil {
		return fmt.Errorf("update conversion job status: %w", err)
	}
	return nil
}

func (r *PgJobRepository) LatestForFile(ctx context.Context, fileID string) (Job, error) {
	var j Job

	err := r.pool.QueryRow(ctx,
		`SELECT id, file_id, retry_of, status FROM conversion_jobs WHERE file_id = $1 ORDER BY created_at DESC LIMIT 1`,
		fileID,
	).Scan(&j.ID, &j.FileID, &j.RetryOf, &j.Status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNoJobs
		}
		return Job{}, fmt.Errorf("find latest conversion job: %w", err)
	}

	return j, nil
}
