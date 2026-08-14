package convert

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"okf-converter/backend/internal/storage"
)

type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	getErr  error
	putErr  error
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string][]byte{}}
}

func (s *fakeObjectStore) EnsureBucket(ctx context.Context) error { return nil }

func (s *fakeObjectStore) PresignedPutURL(ctx context.Context, objectKey string, expiry time.Duration) (string, error) {
	return "", nil
}

func (s *fakeObjectStore) PresignedGetURL(ctx context.Context, objectKey string, expiry time.Duration) (string, error) {
	return "", nil
}

func (s *fakeObjectStore) StatObject(ctx context.Context, objectKey string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, nil
}

func (s *fakeObjectStore) RemoveObject(ctx context.Context, objectKey string) error { return nil }

func (s *fakeObjectStore) GetObject(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}

	s.mu.Lock()
	data, ok := s.objects[objectKey]
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *fakeObjectStore) PutObject(ctx context.Context, objectKey string, r io.Reader, size int64, contentType string) error {
	if s.putErr != nil {
		return s.putErr
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.objects[objectKey] = data
	s.mu.Unlock()
	return nil
}

type fakeStatusUpdater struct {
	mu       sync.Mutex
	statuses []string
}

func (f *fakeStatusUpdater) UpdateStatus(ctx context.Context, id, status string) error {
	f.mu.Lock()
	f.statuses = append(f.statuses, status)
	f.mu.Unlock()
	return nil
}

type recordedOutput struct {
	FileID     string
	ObjectKey  string
	ChunkIndex int
	Size       int64
}

type fakeOutputRecorder struct {
	mu      sync.Mutex
	created []recordedOutput
}

func (f *fakeOutputRecorder) Create(ctx context.Context, fileID, objectKey string, chunkIndex int, size int64) error {
	f.mu.Lock()
	f.created = append(f.created, recordedOutput{FileID: fileID, ObjectKey: objectKey, ChunkIndex: chunkIndex, Size: size})
	f.mu.Unlock()
	return nil
}

func TestSplitConverterProducesOneChunkPerParagraph(t *testing.T) {
	store := newFakeObjectStore()
	store.objects["user-1/src.txt"] = []byte("first paragraph\n\nsecond paragraph\n\nthird paragraph")

	statuses := &fakeStatusUpdater{}
	outputs := &fakeOutputRecorder{}
	conv := NewSplitConverter(store, statuses, outputs)

	job := Job{FileID: "file-1", ObjectKey: "user-1/src.txt", ContentType: "text/plain", OriginalName: "notes.txt"}
	if err := conv.Convert(context.Background(), job); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if len(outputs.created) != 3 {
		t.Fatalf("got %d output chunks, want 3", len(outputs.created))
	}

	for i, out := range outputs.created {
		if out.FileID != "file-1" {
			t.Errorf("chunk %d FileID = %q, want %q", i, out.FileID, "file-1")
		}
		if out.ChunkIndex != i {
			t.Errorf("chunk %d ChunkIndex = %d, want %d", i, out.ChunkIndex, i)
		}
		if _, ok := store.objects[out.ObjectKey]; !ok {
			t.Errorf("chunk %d object %q not stored", i, out.ObjectKey)
		}
	}

	wantStatuses := []string{StatusConverting, StatusConverted}
	if !equalStrings(statuses.statuses, wantStatuses) {
		t.Errorf("statuses = %v, want %v", statuses.statuses, wantStatuses)
	}
}

func TestSplitConverterCSVOneChunkPerRow(t *testing.T) {
	store := newFakeObjectStore()
	store.objects["user-1/src.csv"] = []byte("name,age\nAda,36\n")

	statuses := &fakeStatusUpdater{}
	outputs := &fakeOutputRecorder{}
	conv := NewSplitConverter(store, statuses, outputs)

	job := Job{FileID: "file-2", ObjectKey: "user-1/src.csv", ContentType: "text/csv", OriginalName: "data.csv"}
	if err := conv.Convert(context.Background(), job); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if len(outputs.created) != 2 {
		t.Fatalf("got %d output chunks, want 2", len(outputs.created))
	}
}

func TestSplitConverterUnsupportedFormatMarksFailed(t *testing.T) {
	store := newFakeObjectStore()
	store.objects["user-1/src.zip"] = []byte("junk")

	statuses := &fakeStatusUpdater{}
	outputs := &fakeOutputRecorder{}
	conv := NewSplitConverter(store, statuses, outputs)

	job := Job{FileID: "file-3", ObjectKey: "user-1/src.zip", ContentType: "application/zip", OriginalName: "archive.zip"}
	if err := conv.Convert(context.Background(), job); err == nil {
		t.Fatal("expected error for unsupported format, got nil")
	}

	if len(outputs.created) != 0 {
		t.Errorf("expected no chunks recorded, got %d", len(outputs.created))
	}

	wantStatuses := []string{StatusConverting, StatusFailed}
	if !equalStrings(statuses.statuses, wantStatuses) {
		t.Errorf("statuses = %v, want %v", statuses.statuses, wantStatuses)
	}
}

func TestSplitConverterDownloadFailureMarksFailed(t *testing.T) {
	store := newFakeObjectStore()
	store.getErr = errors.New("boom")

	statuses := &fakeStatusUpdater{}
	outputs := &fakeOutputRecorder{}
	conv := NewSplitConverter(store, statuses, outputs)

	job := Job{FileID: "file-4", ObjectKey: "user-1/src.txt", ContentType: "text/plain", OriginalName: "notes.txt"}
	if err := conv.Convert(context.Background(), job); err == nil {
		t.Fatal("expected error when download fails, got nil")
	}

	wantStatuses := []string{StatusConverting, StatusFailed}
	if !equalStrings(statuses.statuses, wantStatuses) {
		t.Errorf("statuses = %v, want %v", statuses.statuses, wantStatuses)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
