package files

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Output is one file of a generated bundle, as returned to the frontend - a
// signed URL rather than the raw storage key, so a client can open index.md
// or a single concept without downloading the whole package.
type Output struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Position    int    `json:"position"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url"`
}

// OutputRecord is the internal shape the handler reads back from the
// database to build an Output, before the object key is turned into a
// signed URL.
type OutputRecord struct {
	ID        string
	ObjectKey string
	Name      string
	Position  int
	Size      int64
}

type OutputRepository interface {
	Create(ctx context.Context, fileID, objectKey, name string, position int, size int64) error
	ListForFile(ctx context.Context, fileID string) ([]OutputRecord, error)
	// ClearForFile deletes all output rows for fileID. Called at the start
	// of a (re)conversion attempt so a retry never collides with rows left
	// over from a previous attempt, and GET .../outputs always reflects
	// only the most recent attempt.
	ClearForFile(ctx context.Context, fileID string) error
}

type PgOutputRepository struct {
	pool *pgxpool.Pool
}

func NewPgOutputRepository(pool *pgxpool.Pool) *PgOutputRepository {
	return &PgOutputRepository{pool: pool}
}

func (r *PgOutputRepository) Create(ctx context.Context, fileID, objectKey, name string, position int, size int64) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO file_outputs (file_id, object_key, name, position, size) VALUES ($1, $2, $3, $4, $5)`,
		fileID, objectKey, name, position, size,
	)
	if err != nil {
		return fmt.Errorf("create file output: %w", err)
	}
	return nil
}

func (r *PgOutputRepository) ListForFile(ctx context.Context, fileID string) ([]OutputRecord, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, object_key, name, position, size FROM file_outputs WHERE file_id = $1 ORDER BY position`,
		fileID,
	)
	if err != nil {
		return nil, fmt.Errorf("list file outputs: %w", err)
	}
	defer rows.Close()

	var out []OutputRecord
	for rows.Next() {
		var rec OutputRecord
		if err := rows.Scan(&rec.ID, &rec.ObjectKey, &rec.Name, &rec.Position, &rec.Size); err != nil {
			return nil, fmt.Errorf("scan file output: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list file outputs: %w", err)
	}

	return out, nil
}

func (r *PgOutputRepository) ClearForFile(ctx context.Context, fileID string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM file_outputs WHERE file_id = $1`, fileID); err != nil {
		return fmt.Errorf("clear file outputs: %w", err)
	}
	return nil
}
