// Package storage wraps MinIO object storage access behind a small
// interface, so file-handling code (and its tests) don't depend on a live
// MinIO instance or the minio-go client type directly.
package storage

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"okf-converter/backend/internal/config"
)

type ObjectInfo struct {
	Size int64
}

type Storage interface {
	// EnsureBucket creates the configured bucket if it doesn't already exist.
	EnsureBucket(ctx context.Context) error
	// PresignedPutURL returns a browser-usable presigned PUT URL, signed
	// against the *public* endpoint since the client follows it directly.
	PresignedPutURL(ctx context.Context, objectKey string, expiry time.Duration) (string, error)
	// PresignedGetURL returns a browser-usable presigned GET URL, signed
	// against the *public* endpoint for the same reason as PresignedPutURL.
	PresignedGetURL(ctx context.Context, objectKey string, expiry time.Duration) (string, error)
	StatObject(ctx context.Context, objectKey string) (ObjectInfo, error)
	RemoveObject(ctx context.Context, objectKey string) error
	// GetObject and PutObject are server-to-server transfers (used by the
	// conversion pipeline to read the source upload and write its output
	// chunks), so - unlike the presigned URLs - they go through the
	// internal client.
	GetObject(ctx context.Context, objectKey string) (io.ReadCloser, error)
	PutObject(ctx context.Context, objectKey string, r io.Reader, size int64, contentType string) error
}

// MinioStorage holds two clients, mirroring the previous split: internal
// (Docker-network hostname, used for server-to-server calls) and public
// (browser-reachable, used only for signing PUT URLs).
type MinioStorage struct {
	internal *minio.Client
	public   *minio.Client
	bucket   string
	region   string
}

func New(cfg config.MinioConfig) (*MinioStorage, error) {
	internal, err := minio.New(fmt.Sprintf("%s:%d", cfg.Endpoint, cfg.Port), &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create internal minio client: %w", err)
	}

	public, err := minio.New(fmt.Sprintf("%s:%d", cfg.PublicEndpoint, cfg.PublicPort), &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.PublicUseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create public minio client: %w", err)
	}

	return &MinioStorage{
		internal: internal,
		public:   public,
		bucket:   cfg.Bucket,
		region:   cfg.Region,
	}, nil
}

func (s *MinioStorage) EnsureBucket(ctx context.Context) error {
	exists, err := s.internal.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket exists: %w", err)
	}

	if exists {
		log.Printf("MinIO bucket exists: %s", s.bucket)
		return nil
	}

	if err := s.internal.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region}); err != nil {
		return fmt.Errorf("create bucket: %w", err)
	}

	log.Printf("Created MinIO bucket: %s", s.bucket)
	return nil
}

func (s *MinioStorage) PresignedPutURL(ctx context.Context, objectKey string, expiry time.Duration) (string, error) {
	u, err := s.public.PresignedPutObject(ctx, s.bucket, objectKey, expiry)
	if err != nil {
		return "", fmt.Errorf("presign put object: %w", err)
	}
	return u.String(), nil
}

func (s *MinioStorage) PresignedGetURL(ctx context.Context, objectKey string, expiry time.Duration) (string, error) {
	u, err := s.public.PresignedGetObject(ctx, s.bucket, objectKey, expiry, nil)
	if err != nil {
		return "", fmt.Errorf("presign get object: %w", err)
	}
	return u.String(), nil
}

func (s *MinioStorage) GetObject(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	obj, err := s.internal.GetObject(ctx, s.bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}

	// GetObject doesn't itself make a request - StatObject forces one, so a
	// missing object is reported here rather than as an opaque failure on
	// first Read from the caller.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, fmt.Errorf("get object: %w", err)
	}

	return obj, nil
}

func (s *MinioStorage) PutObject(ctx context.Context, objectKey string, r io.Reader, size int64, contentType string) error {
	_, err := s.internal.PutObject(ctx, s.bucket, objectKey, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

func (s *MinioStorage) StatObject(ctx context.Context, objectKey string) (ObjectInfo, error) {
	info, err := s.internal.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("stat object: %w", err)
	}
	return ObjectInfo{Size: info.Size}, nil
}

func (s *MinioStorage) RemoveObject(ctx context.Context, objectKey string) error {
	if err := s.internal.RemoveObject(ctx, s.bucket, objectKey, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("remove object: %w", err)
	}
	return nil
}

// CreateObjectName builds a per-user, collision-resistant object key:
// <userID>/<uuid>.<ext>. Unlike the previous JS implementation (which fell
// back to using the whole filename as a bogus "extension" when there was no
// '.' in it, due to Array.pop() on a single-element split), a filename with
// no extension here simply produces <userID>/<uuid> - this is internal
// storage-key detail only, never exposed to the frontend.
func CreateObjectName(userID, originalName string) string {
	ext := ""
	if idx := strings.LastIndex(originalName, "."); idx != -1 && idx != len(originalName)-1 {
		ext = strings.ToLower(originalName[idx+1:])
	}

	id := uuid.NewString()

	if ext == "" {
		return fmt.Sprintf("%s/%s", userID, id)
	}
	return fmt.Sprintf("%s/%s.%s", userID, id, ext)
}

// ResultObjectName derives the deterministic key for a source object's
// bundled result zip, e.g. "<userID>/<uuid>.<ext>" -> "<userID>/<uuid>-result.zip".
// Being a pure function of objectKey (rather than a stored value) lets both
// the handler that presigns the download URL and the worker that writes the
// zip agree on the key without any coordination.
func ResultObjectName(objectKey string) string {
	base := objectKey
	if idx := strings.LastIndex(objectKey, "."); idx != -1 {
		base = objectKey[:idx]
	}
	return base + "-result.zip"
}
