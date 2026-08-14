package files

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Output is one converted chunk file, as returned to the frontend - a
// signed URL rather than the raw storage key.
type Output struct {
	ID          string `json:"id"`
	ChunkIndex  int    `json:"chunk_index"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url"`
}

// OutputRecord is the internal shape the handler reads back from storage to
// build an Output, before the object key is turned into a signed URL.
type OutputRecord struct {
	ID         string
	ObjectKey  string
	ChunkIndex int
	Size       int64
}

type OutputRepository interface {
	Create(ctx context.Context, fileID, objectKey string, chunkIndex int, size int64) error
	ListForFile(ctx context.Context, fileID string) ([]OutputRecord, error)
}

type PgOutputRepository struct {
	pool *pgxpool.Pool
}

func NewPgOutputRepository(pool *pgxpool.Pool) *PgOutputRepository {
	return &PgOutputRepository{pool: pool}
}

func (r *PgOutputRepository) Create(ctx context.Context, fileID, objectKey string, chunkIndex int, size int64) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO file_outputs (file_id, object_key, chunk_index, size) VALUES ($1, $2, $3, $4)`,
		fileID, objectKey, chunkIndex, size,
	)
	if err != nil {
		return fmt.Errorf("create file output: %w", err)
	}
	return nil
}

func (r *PgOutputRepository) ListForFile(ctx context.Context, fileID string) ([]OutputRecord, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, object_key, chunk_index, size FROM file_outputs WHERE file_id = $1 ORDER BY chunk_index`,
		fileID,
	)
	if err != nil {
		return nil, fmt.Errorf("list file outputs: %w", err)
	}
	defer rows.Close()

	var out []OutputRecord
	for rows.Next() {
		var rec OutputRecord
		if err := rows.Scan(&rec.ID, &rec.ObjectKey, &rec.ChunkIndex, &rec.Size); err != nil {
			return nil, fmt.Errorf("scan file output: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list file outputs: %w", err)
	}

	return out, nil
}
