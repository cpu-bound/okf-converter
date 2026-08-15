package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	f.records[id] = FileRecord{ID: id, ObjectKey: objectKey, OriginalName: originalName, ContentType: contentType, Size: size, Status: "pending"}
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
	rec := f.records[id]
	rec.Status = "ready"
	f.records[id] = rec
	return file, nil
}

// setStatus is a white-box test helper (production status transitions
// beyond MarkReady/MarkRetrying happen out-of-process, in the conversion
// worker) for putting a file into a given state before exercising a
// handler.
func (f *fakeFileRepository) setStatus(id, status string) {
	file := f.files[id]
	file.Status = status
	f.files[id] = file
	rec := f.records[id]
	rec.Status = status
	f.records[id] = rec
}

func (f *fakeFileRepository) MarkRetrying(ctx context.Context, id string) (File, bool, error) {
	file, ok := f.files[id]
	if !ok {
		return File{}, false, ErrNotFound
	}
	if file.Status != "failed" {
		return file, false, nil
	}
	f.setStatus(id, "converting")
	return f.files[id], true, nil
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

func (f *fakeOutputRepository) ClearForFile(ctx context.Context, fileID string) error {
	delete(f.records, fileID)
	return nil
}

type fakeJobRepository struct {
	byFile map[string][]Job
	nextID int
}

func newFakeJobRepository() *fakeJobRepository {
	return &fakeJobRepository{byFile: map[string][]Job{}}
}

func (f *fakeJobRepository) Create(ctx context.Context, fileID string, retryOf *string) (Job, error) {
	f.nextID++
	j := Job{ID: "job-" + strconv.Itoa(f.nextID), FileID: fileID, RetryOf: retryOf, Status: "queued"}
	f.byFile[fileID] = append(f.byFile[fileID], j)
	return j, nil
}

func (f *fakeJobRepository) UpdateStatus(ctx context.Context, jobID, status string, errMsg *string) error {
	for fileID, jobs := range f.byFile {
		for i, j := range jobs {
			if j.ID == jobID {
				jobs[i].Status = status
				f.byFile[fileID] = jobs
				return nil
			}
		}
	}
	return nil
}

func (f *fakeJobRepository) LatestForFile(ctx context.Context, fileID string) (Job, error) {
	jobs := f.byFile[fileID]
	if len(jobs) == 0 {
		return Job{}, ErrNoJobs
	}
	return jobs[len(jobs)-1], nil
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
		{"too large", fmt.Sprintf(`{"filename":"report.pdf","contentType":"application/pdf","size":%d}`, MaxFileSize+1), http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeFileRepository()
			store := &fakeStorage{presignedOK: "https://minio.example/presigned"}
			h := NewHandlers(repo, newFakeOutputRepository(), newFakeJobRepository(), store)

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
		store := &fakeStorage{statSize: 1024, presignedOK: "https://minio.example/result"}
		jobs := newFakeJobRepository()
		h := NewHandlers(repo, newFakeOutputRepository(), jobs, store)

		var enqueued bool
		h.EnqueueConversion = func(ctx context.Context, f File, objectKey string, retryOf *string) {
			enqueued = true
			if retryOf != nil {
				t.Errorf("retryOf = %v, want nil on first confirm", *retryOf)
			}
		}

		req := requestAsUser(http.MethodPost, "/api/files/"+file.ID+"/confirm", "", user)
		req.SetPathValue("id", file.ID)
		rec := httptest.NewRecorder()

		h.Confirm(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
		}

		var resp struct {
			File      File   `json:"file"`
			ResultURL string `json:"resultUrl"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.File.Status != "ready" {
			t.Errorf("status = %q, want %q", resp.File.Status, "ready")
		}
		if resp.ResultURL != store.presignedOK {
			t.Errorf("resultUrl = %q, want %q", resp.ResultURL, store.presignedOK)
		}
		if !enqueued {
			t.Error("expected EnqueueConversion to be called")
		}
	})

	t.Run("size mismatch, deletes and 409s", func(t *testing.T) {
		repo := newFakeFileRepository()
		file, _ := repo.Create(context.Background(), user.ID, "user-1/abc.pdf", "report.pdf", "application/pdf", 1024)
		store := &fakeStorage{statSize: 999}
		h := NewHandlers(repo, newFakeOutputRepository(), newFakeJobRepository(), store)

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
		h := NewHandlers(repo, newFakeOutputRepository(), newFakeJobRepository(), store)

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
		h := NewHandlers(repo, newFakeOutputRepository(), newFakeJobRepository(), store)

		req := requestAsUser(http.MethodPost, "/api/files/does-not-exist/confirm", "", user)
		req.SetPathValue("id", "does-not-exist")
		rec := httptest.NewRecorder()

		h.Confirm(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})
}

func TestStatusHandler(t *testing.T) {
	user := auth.User{ID: "user-1"}

	repo := newFakeFileRepository()
	file, _ := repo.Create(context.Background(), user.ID, "user-1/abc.pdf", "report.pdf", "application/pdf", 1024)
	repo.setStatus(file.ID, "converting")

	h := NewHandlers(repo, newFakeOutputRepository(), newFakeJobRepository(), &fakeStorage{})

	req := requestAsUser(http.MethodGet, "/api/files/"+file.ID, "", user)
	req.SetPathValue("id", file.ID)
	rec := httptest.NewRecorder()

	h.Status(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		File File `json:"file"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.File.Status != "converting" {
		t.Errorf("status = %q, want %q", resp.File.Status, "converting")
	}
	if resp.File.OriginalName != "report.pdf" {
		t.Errorf("original_name = %q, want %q", resp.File.OriginalName, "report.pdf")
	}
}

// TestRetryHandlerIsIdempotent drives two retry calls (as a duplicated /
// retried client request would) against the same failed file and asserts
// only the first actually wins the failed->converting transition and
// enqueues a job - the second observes the same result with no further
// side effects, so repeating the request is always safe. (The real
// PgFileRepository.MarkRetrying gets this same guarantee under true
// concurrency for free from Postgres's row-level locking on the CAS
// UPDATE; the fakes here aren't goroutine-safe, so this test exercises the
// handler's idempotency contract sequentially rather than racing it.)
func TestRetryHandlerIsIdempotent(t *testing.T) {
	user := auth.User{ID: "user-1"}

	repo := newFakeFileRepository()
	file, _ := repo.Create(context.Background(), user.ID, "user-1/abc.pdf", "report.pdf", "application/pdf", 1024)
	repo.setStatus(file.ID, "failed")

	jobs := newFakeJobRepository()
	firstJob, _ := jobs.Create(context.Background(), file.ID, nil)
	jobs.UpdateStatus(context.Background(), firstJob.ID, "failed", nil)

	store := &fakeStorage{presignedOK: "https://minio.example/result"}
	h := NewHandlers(repo, newFakeOutputRepository(), jobs, store)

	// Mirrors main.go's real EnqueueConversion closure: recording the
	// attempt (jobs.Create) is the callback's job, not the handler's -
	// Retry only decides *whether* to call it (via the CAS) and *what*
	// retryOf to pass.
	var enqueuedRetryOf []*string
	h.EnqueueConversion = func(ctx context.Context, f File, objectKey string, retryOf *string) {
		if _, err := jobs.Create(ctx, f.ID, retryOf); err != nil {
			t.Fatalf("jobs.Create: %v", err)
		}
		enqueuedRetryOf = append(enqueuedRetryOf, retryOf)
	}

	run := func() *httptest.ResponseRecorder {
		req := requestAsUser(http.MethodPost, "/api/files/"+file.ID+"/retry", "", user)
		req.SetPathValue("id", file.ID)
		rec := httptest.NewRecorder()
		h.Retry(rec, req)
		return rec
	}

	first := run()
	second := run()

	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("status = %d, %d, want both %d", first.Code, second.Code, http.StatusOK)
	}

	var firstResp, secondResp struct {
		File      File   `json:"file"`
		ResultURL string `json:"resultUrl"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondResp); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if firstResp.File.Status != secondResp.File.Status || firstResp.ResultURL != secondResp.ResultURL {
		t.Errorf("responses diverged: %+v vs %+v", firstResp, secondResp)
	}

	if len(enqueuedRetryOf) != 1 {
		t.Fatalf("expected exactly one enqueued retry, got %d", len(enqueuedRetryOf))
	}
	if enqueuedRetryOf[0] == nil || *enqueuedRetryOf[0] != firstJob.ID {
		t.Errorf("retryOf = %v, want %q", enqueuedRetryOf[0], firstJob.ID)
	}

	if got := len(jobs.byFile[file.ID]); got != 2 {
		t.Errorf("got %d conversion_jobs rows for file, want 2 (original + retry)", got)
	}
}

func TestRetryHandlerNoOpWhenNotFailed(t *testing.T) {
	user := auth.User{ID: "user-1"}

	repo := newFakeFileRepository()
	file, _ := repo.Create(context.Background(), user.ID, "user-1/abc.pdf", "report.pdf", "application/pdf", 1024)
	repo.setStatus(file.ID, "converting")

	jobs := newFakeJobRepository()
	store := &fakeStorage{presignedOK: "https://minio.example/result"}
	h := NewHandlers(repo, newFakeOutputRepository(), jobs, store)

	var enqueued bool
	h.EnqueueConversion = func(ctx context.Context, f File, objectKey string, retryOf *string) {
		enqueued = true
	}

	req := requestAsUser(http.MethodPost, "/api/files/"+file.ID+"/retry", "", user)
	req.SetPathValue("id", file.ID)
	rec := httptest.NewRecorder()
	h.Retry(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if enqueued {
		t.Error("expected no job to be enqueued for a file that wasn't failed")
	}
	if len(jobs.byFile[file.ID]) != 0 {
		t.Errorf("expected no conversion_jobs rows to be created, got %d", len(jobs.byFile[file.ID]))
	}
}
