package convert

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"okf-converter/backend/internal/bundle"
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
	FileID    string
	ObjectKey string
	Name      string
	Position  int
	Size      int64
}

type fakeOutputRecorder struct {
	mu       sync.Mutex
	created  []recordedOutput
	clearedN int
}

func (f *fakeOutputRecorder) Create(ctx context.Context, fileID, objectKey, name string, position int, size int64) error {
	f.mu.Lock()
	f.created = append(f.created, recordedOutput{FileID: fileID, ObjectKey: objectKey, Name: name, Position: position, Size: size})
	f.mu.Unlock()
	return nil
}

// ClearForFile mimics the real DELETE FROM file_outputs WHERE file_id = $1:
// it drops every recorded file for fileID (regardless of which attempt
// produced it), so a retried job starts from a clean slate.
func (f *fakeOutputRecorder) ClearForFile(ctx context.Context, fileID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearedN++
	kept := f.created[:0]
	for _, o := range f.created {
		if o.FileID != fileID {
			kept = append(kept, o)
		}
	}
	f.created = kept
	return nil
}

func (f *fakeOutputRecorder) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]string, 0, len(f.created))
	for _, o := range f.created {
		out = append(out, o.Name)
	}
	return out
}

type fakeJobStatusUpdater struct {
	mu       sync.Mutex
	statuses []string
}

func (f *fakeJobStatusUpdater) UpdateStatus(ctx context.Context, jobID, status string, errMsg *string) error {
	f.mu.Lock()
	f.statuses = append(f.statuses, status)
	f.mu.Unlock()
	return nil
}

type harness struct {
	store    *fakeObjectStore
	statuses *fakeStatusUpdater
	outputs  *fakeOutputRecorder
	jobs     *fakeJobStatusUpdater
	conv     *BundleConverter
}

func newHarness(objects map[string]string) *harness {
	store := newFakeObjectStore()
	for key, content := range objects {
		store.objects[key] = []byte(content)
	}

	h := &harness{
		store:    store,
		statuses: &fakeStatusUpdater{},
		outputs:  &fakeOutputRecorder{},
		jobs:     &fakeJobStatusUpdater{},
	}
	h.conv = NewBundleConverter(store, h.statuses, h.outputs, h.jobs)
	return h
}

// bundleFile returns the stored content of one file of the bundle produced
// for objectKey.
func (h *harness) bundleFile(objectKey, name string) (string, bool) {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()

	data, ok := h.store.objects[storage.BundleObjectPrefix(objectKey)+"/"+name]
	return string(data), ok
}

func TestConvertProducesAnOKFBundle(t *testing.T) {
	const src = "user-1/src.md"
	h := newHarness(map[string]string{
		src: "# Introducción\n\nprimer párrafo\n\n# Metodología\n\nsegundo párrafo\n\n# Conclusiones\n\ntercero",
	})

	job := Job{JobID: "job-1", FileID: "file-1", ObjectKey: src, ContentType: "text/markdown", OriginalName: "notas-de-clase.md", Size: 42}
	if err := h.conv.Convert(context.Background(), job); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	// The minimum structure the spec requires, present regardless of what
	// the source document looked like.
	for _, required := range []string{bundle.IndexFile, bundle.LogFile} {
		if _, ok := h.bundleFile(src, required); !ok {
			t.Errorf("bundle is missing %s", required)
		}
	}

	names := h.outputs.names()
	if len(names) != 5 {
		t.Fatalf("recorded bundle files = %v, want index.md + log.md + 3 concepts", names)
	}
	if names[0] != bundle.IndexFile || names[1] != bundle.LogFile {
		t.Errorf("bundle files = %v, want index.md and log.md first", names)
	}

	index, _ := h.bundleFile(src, bundle.IndexFile)

	// index.md must link every concept, in the source document's order.
	for _, name := range names[2:] {
		if !strings.Contains(index, "("+name+")") {
			t.Errorf("index.md does not link %q:\n%s", name, index)
		}
	}
	if first, second := strings.Index(index, names[2]), strings.Index(index, names[3]); first > second {
		t.Errorf("index.md lists concepts out of document order:\n%s", index)
	}

	// Titles come from the document's own headings.
	for _, want := range []string{"Introducción", "Metodología", "Conclusiones"} {
		if !strings.Contains(index, want) {
			t.Errorf("index.md does not carry the section title %q:\n%s", want, index)
		}
	}

	log, _ := h.bundleFile(src, bundle.LogFile)
	for _, want := range []string{"job-1", "Operaciones", "Unidades detectadas", "formato de origen detectado"} {
		if !strings.Contains(log, want) {
			t.Errorf("log.md does not mention %q:\n%s", want, log)
		}
	}

	wantStatuses := []string{StatusConverting, StatusConverted}
	if !equalStrings(h.statuses.statuses, wantStatuses) {
		t.Errorf("statuses = %v, want %v", h.statuses.statuses, wantStatuses)
	}
	if !equalStrings(h.jobs.statuses, wantStatuses) {
		t.Errorf("job statuses = %v, want %v", h.jobs.statuses, wantStatuses)
	}
}

// A short document with no divisions is an ordinary bundle, not a special
// case: it still gets index.md, log.md and exactly one concept, and the
// conversion succeeds without complaint.
func TestConvertShortDocumentProducesSingleConcept(t *testing.T) {
	const src = "user-1/short.txt"
	h := newHarness(map[string]string{src: "una sola línea, sin divisiones"})

	job := Job{JobID: "job-1", FileID: "file-1", ObjectKey: src, ContentType: "text/plain", OriginalName: "breve.txt"}
	if err := h.conv.Convert(context.Background(), job); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	names := h.outputs.names()
	if len(names) != 3 {
		t.Fatalf("bundle files = %v, want index.md + log.md + exactly 1 concept", names)
	}

	index, _ := h.bundleFile(src, bundle.IndexFile)
	if !strings.Contains(index, "("+names[2]+")") {
		t.Errorf("index.md does not link the only concept %q:\n%s", names[2], index)
	}
}

func TestConvertPackagesBundleAsArchive(t *testing.T) {
	const src = "user-1/src.txt"
	h := newHarness(map[string]string{src: "primero\n\nsegundo"})

	job := Job{JobID: "job-1", FileID: "file-1", ObjectKey: src, ContentType: "text/plain", OriginalName: "apuntes.txt"}
	if err := h.conv.Convert(context.Background(), job); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	archive, ok := h.store.objects[storage.ResultObjectName(src)]
	if !ok {
		t.Fatalf("bundle archive not stored at %q", storage.ResultObjectName(src))
	}

	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("bundle archive is not a valid zip: %v", err)
	}

	if len(zr.File) != len(h.outputs.names()) {
		t.Errorf("archive holds %d entries, want %d (every file of the bundle)", len(zr.File), len(h.outputs.names()))
	}

	// Everything sits under a single root folder, so extracting the archive
	// yields one self-contained directory.
	root, _, found := strings.Cut(zr.File[0].Name, "/")
	if !found {
		t.Fatalf("archive entry %q is not under a bundle root folder", zr.File[0].Name)
	}
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, root+"/") {
			t.Errorf("archive entry %q is outside the bundle root %q", f.Name, root)
		}
	}

	if zr.File[0].Name != root+"/"+bundle.IndexFile {
		t.Errorf("first archive entry = %q, want %q", zr.File[0].Name, root+"/"+bundle.IndexFile)
	}
}

func TestConvertRetryIsIdempotent(t *testing.T) {
	const src = "user-1/src.txt"
	h := newHarness(map[string]string{src: "primero\n\nsegundo\n\ntercero"})

	firstAttempt := Job{JobID: "job-1", FileID: "file-1", ObjectKey: src, ContentType: "text/plain", OriginalName: "notas.txt"}
	if err := h.conv.Convert(context.Background(), firstAttempt); err != nil {
		t.Fatalf("first Convert() error = %v", err)
	}
	firstNames := h.outputs.names()

	// Same FileID/ObjectKey as a retry would use (deterministic keys), just
	// a different JobID for the new attempt.
	retryAttempt := Job{JobID: "job-2", FileID: "file-1", ObjectKey: src, ContentType: "text/plain", OriginalName: "notas.txt"}
	if err := h.conv.Convert(context.Background(), retryAttempt); err != nil {
		t.Fatalf("retry Convert() error = %v", err)
	}

	if h.outputs.clearedN != 2 {
		t.Errorf("ClearForFile called %d times, want 2 (once per attempt)", h.outputs.clearedN)
	}
	if got := h.outputs.names(); !equalStrings(got, firstNames) {
		t.Errorf("bundle files after retry = %v, want the same %v (no duplicates left over)", got, firstNames)
	}
	if _, ok := h.store.objects[storage.ResultObjectName(src)]; !ok {
		t.Fatalf("bundle archive not stored after retry")
	}
}

func TestConvertCSVOneUnitPerRow(t *testing.T) {
	const src = "user-1/src.csv"
	h := newHarness(map[string]string{src: "name,age\nAda,36\n"})

	job := Job{JobID: "job-2", FileID: "file-2", ObjectKey: src, ContentType: "text/csv", OriginalName: "data.csv"}
	if err := h.conv.Convert(context.Background(), job); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if names := h.outputs.names(); len(names) != 4 {
		t.Fatalf("bundle files = %v, want index.md + log.md + 2 concepts", names)
	}
}

func TestConvertUnsupportedFormatMarksFailed(t *testing.T) {
	h := newHarness(map[string]string{"user-1/src.zip": "junk"})

	job := Job{JobID: "job-3", FileID: "file-3", ObjectKey: "user-1/src.zip", ContentType: "application/zip", OriginalName: "archive.zip"}
	if err := h.conv.Convert(context.Background(), job); err == nil {
		t.Fatal("expected error for unsupported format, got nil")
	}

	if len(h.outputs.created) != 0 {
		t.Errorf("expected no bundle files recorded, got %d", len(h.outputs.created))
	}

	wantStatuses := []string{StatusConverting, StatusFailed}
	if !equalStrings(h.statuses.statuses, wantStatuses) {
		t.Errorf("statuses = %v, want %v", h.statuses.statuses, wantStatuses)
	}
}

func TestConvertDownloadFailureMarksFailed(t *testing.T) {
	h := newHarness(nil)
	h.store.getErr = errors.New("boom")

	job := Job{JobID: "job-4", FileID: "file-4", ObjectKey: "user-1/src.txt", ContentType: "text/plain", OriginalName: "notas.txt"}
	if err := h.conv.Convert(context.Background(), job); err == nil {
		t.Fatal("expected error when download fails, got nil")
	}

	wantStatuses := []string{StatusConverting, StatusFailed}
	if !equalStrings(h.statuses.statuses, wantStatuses) {
		t.Errorf("statuses = %v, want %v", h.statuses.statuses, wantStatuses)
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
