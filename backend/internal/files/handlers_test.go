package files

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"okf-converter/backend/internal/auth"
	"okf-converter/backend/internal/middleware"
	"okf-converter/backend/internal/storage"
)

type fakeFileRepository struct {
	records map[string]FileRecord
	files   map[string]File
	nextID  int
	deleted []string
}

func newFakeFileRepository() *fakeFileRepository {
	return &fakeFileRepository{records: map[string]FileRecord{}, files: map[string]File{}}
}

func (f *fakeFileRepository) Create(ctx context.Context, userID, objectKey, originalName, contentType string, size int64) (File, error) {
	f.nextID++
	id := "file-" + strconv.Itoa(f.nextID)
	file := File{ID: id, OriginalName: originalName, ContentType: contentType, Size: size, Status: "pending"}
	f.files[id] = file
	f.records[id] = FileRecord{ID: id, ObjectKey: objectKey, Size: size, Status: "pending"}
	return file, nil
}

func (f *fakeFileRepository) FindForUser(ctx context.Context, id, userID string) (FileRecord, error) {
	rec, ok := f.records[id]
	if !ok {
		return FileRecord{}, ErrNotFound
	}
	return rec, nil
}

func (f *fakeFileRepository) MarkReady(ctx context.Context, id string) (File, error) {
	file := f.files[id]
	file.Status = "ready"
	f.files[id] = file
	return file, nil
}

func (f *fakeFileRepository) Delete(ctx context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.records, id)
	delete(f.files, id)
	return nil
}

type fakeOutputRepository struct {
	records map[string][]OutputRecord
}

func newFakeOutputRepository() *fakeOutputRepository {
	return &fakeOutputRepository{records: map[string][]OutputRecord{}}
}

func (f *fakeOutputRepository) Create(ctx context.Context, fileID, objectKey string, chunkIndex int, size int64) error {
	f.records[fileID] = append(f.records[fileID], OutputRecord{ID: objectKey, ObjectKey: objectKey, ChunkIndex: chunkIndex, Size: size})
	return nil
}

func (f *fakeOutputRepository) ListForFile(ctx context.Context, fileID string) ([]OutputRecord, error) {
	return f.records[fileID], nil
}

type fakeStorage struct {
	statSize    int64
	statErr     error
	removed     []string
	presignedOK string
}

func (s *fakeStorage) EnsureBucket(ctx context.Context) error { return nil }

func (s *fakeStorage) PresignedPutURL(ctx context.Context, objectKey string, expiry time.Duration) (string, error) {
	return s.presignedOK, nil
}

func (s *fakeStorage) PresignedGetURL(ctx context.Context, objectKey string, expiry time.Duration) (string, error) {
	return s.presignedOK, nil
}

func (s *fakeStorage) StatObject(ctx context.Context, objectKey string) (storage.ObjectInfo, error) {
	if s.statErr != nil {
		return storage.ObjectInfo{}, s.statErr
	}
	return storage.ObjectInfo{Size: s.statSize}, nil
}

func (s *fakeStorage) RemoveObject(ctx context.Context, objectKey string) error {
	s.removed = append(s.removed, objectKey)
	return nil
}

func (s *fakeStorage) GetObject(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (s *fakeStorage) PutObject(ctx context.Context, objectKey string, r io.Reader, size int64, contentType string) error {
	return nil
}

// requestAsUser builds a request whose context already carries user, by
// running it through the real RequireAuth middleware (with a stub loader)
// so tests exercise the same context-injection path production traffic does.
func requestAsUser(method, target string, body string, user auth.User) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()

	var captured *http.Request
	loader := staticLoader{user: user}
	middleware.RequireAuth(loader)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
	})).ServeHTTP(rec, req)

	return captured
}

type staticLoader struct{ user auth.User }

func (l staticLoader) UserFromRequest(r *http.Request) (auth.User, error) { return l.user, nil }

func TestUploadURLHandler(t *testing.T) {
	user := auth.User{ID: "user-1", Name: "Ada", Email: "a@example.com"}

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"valid request", `{"filename":"report.pdf","contentType":"application/pdf","size":1024}`, http.StatusOK},
		{"missing size", `{"filename":"report.pdf","contentType":"application/pdf"}`, http.StatusBadRequest},
		{"zero size", `{"filename":"report.pdf","contentType":"application/pdf","size":0}`, http.StatusBadRequest},
		{"too large", `{"filename":"report.pdf","contentType":"application/pdf","size":26214401}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeFileRepository()
			store := &fakeStorage{presignedOK: "https://minio.example/presigned"}
			h := NewHandlers(repo, newFakeOutputRepository(), store)

			req := requestAsUser(http.MethodPost, "/api/files/upload-url", tt.body, user)
			rec := httptest.NewRecorder()

			h.UploadURL(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}

			if tt.wantStatus == http.StatusOK {
				var resp struct {
					File      File   `json:"file"`
					UploadURL string `json:"uploadUrl"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if resp.UploadURL != store.presignedOK {
					t.Errorf("uploadUrl = %q, want %q", resp.UploadURL, store.presignedOK)
				}
				if resp.File.OriginalName != "report.pdf" {
					t.Errorf("file.original_name = %q, want %q", resp.File.OriginalName, "report.pdf")
				}
			}
		})
	}
}

func TestConfirmHandler(t *testing.T) {
	user := auth.User{ID: "user-1"}

	t.Run("size matches, marks ready", func(t *testing.T) {
		repo := newFakeFileRepository()
		file, _ := repo.Create(context.Background(), user.ID, "user-1/abc.pdf", "report.pdf", "application/pdf", 1024)
		store := &fakeStorage{statSize: 1024}
		h := NewHandlers(repo, newFakeOutputRepository(), store)

		req := requestAsUser(http.MethodPost, "/api/files/"+file.ID+"/confirm", "", user)
		req.SetPathValue("id", file.ID)
		rec := httptest.NewRecorder()

		h.Confirm(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp struct {
			File File `json:"file"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.File.Status != "ready" {
			t.Errorf("status = %q, want %q", resp.File.Status, "ready")
		}
	})

	t.Run("size mismatch, deletes and 409s", func(t *testing.T) {
		repo := newFakeFileRepository()
		file, _ := repo.Create(context.Background(), user.ID, "user-1/abc.pdf", "report.pdf", "application/pdf", 1024)
		store := &fakeStorage{statSize: 999}
		h := NewHandlers(repo, newFakeOutputRepository(), store)

		req := requestAsUser(http.MethodPost, "/api/files/"+file.ID+"/confirm", "", user)
		req.SetPathValue("id", file.ID)
		rec := httptest.NewRecorder()

		h.Confirm(rec, req)

		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
		}
		if len(store.removed) != 1 {
			t.Errorf("expected object to be removed, got %v", store.removed)
		}
		if len(repo.deleted) != 1 {
			t.Errorf("expected file row to be deleted, got %v", repo.deleted)
		}
	})

	t.Run("object missing in storage, 409s", func(t *testing.T) {
		repo := newFakeFileRepository()
		file, _ := repo.Create(context.Background(), user.ID, "user-1/abc.pdf", "report.pdf", "application/pdf", 1024)
		store := &fakeStorage{statErr: errors.New("not found")}
		h := NewHandlers(repo, newFakeOutputRepository(), store)

		req := requestAsUser(http.MethodPost, "/api/files/"+file.ID+"/confirm", "", user)
		req.SetPathValue("id", file.ID)
		rec := httptest.NewRecorder()

		h.Confirm(rec, req)

		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
		}
	})

	t.Run("unknown file, 404s", func(t *testing.T) {
		repo := newFakeFileRepository()
		store := &fakeStorage{}
		h := NewHandlers(repo, newFakeOutputRepository(), store)

		req := requestAsUser(http.MethodPost, "/api/files/does-not-exist/confirm", "", user)
		req.SetPathValue("id", "does-not-exist")
		rec := httptest.NewRecorder()

		h.Confirm(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})
}
