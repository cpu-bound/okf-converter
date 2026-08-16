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

// Claim atomically takes ownership of a conversion job, and is what makes a
// duplicated queue delivery harmless: whichever worker wins the UPDATE
// converts, and every other delivery of the same job is told claimed=false
// and simply acks. Without it, RabbitMQ redelivering a message would convert
// the same document twice (§6: one final effect, at most one published
// bundle).
//
// Which states may be claimed:
//
//   - 'queued'  — a fresh job, or one put back by Requeue for another attempt.
//   - 'failed'  — the previous attempt finished and lost; retrying is the point.
//   - 'converting' — only when the broker marked the delivery as redelivered.
//     RabbitMQ only redelivers once the previous consumer's channel is gone,
//     so a job stuck in 'converting' with a redelivered message is one whose
//     worker died mid-conversion; refusing it would strand the file forever.
//     A merely duplicated *publish* is not flagged as redelivered, so this
//     does not weaken the guarantee above.
//   - 'converted' is never claimable: the bundle is already published, and
//     publishing a second one is exactly what §6 forbids.
//
// attempts is the post-increment count, so the first successful claim of a
// job returns 1.
func (r *PgJobRepository) Claim(ctx context.Context, jobID string, redelivered bool) (attempts int, claimed bool, err error) {
	err = r.pool.QueryRow(ctx,
		`
		UPDATE conversion_jobs
		SET status = 'converting',
		    attempts = attempts + 1,
		    error = NULL,
		    finished_at = NULL
		WHERE id = $1
		  AND status <> 'converted'
		  AND (status <> 'converting' OR $2)
		RETURNING attempts
		`,
		jobID, redelivered,
	).Scan(&attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("claim conversion job: %w", err)
	}

	return attempts, true, nil
}

// Requeue puts a job that failed but will be retried automatically back into
// a waiting state, along with its file. Without it the file would sit at
// 'failed' between attempts, and both the API and the dashboard would report
// a final failure for work that is still in progress - the dashboard would
// even stop polling, since 'failed' is a terminal state to it.
//
// The file goes back to 'ready' rather than 'converting' because that is what
// it actually is: waiting in the queue for a worker, not being converted.
func (r *PgJobRepository) Requeue(ctx context.Context, jobID, fileID string, lastErr string) error {
	batch := &pgx.Batch{}
	batch.Queue(
		`UPDATE conversion_jobs SET status = 'queued', error = $2, finished_at = NULL WHERE id = $1`,
		jobID, lastErr,
	)
	batch.Queue(`UPDATE files SET status = 'ready' WHERE id = $1`, fileID)

	if err := r.pool.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("requeue conversion job: %w", err)
	}
	return nil
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
