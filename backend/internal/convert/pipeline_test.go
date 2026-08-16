package convert

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
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
	verdict  string
	report   []byte
	clearedN int
}

func (f *fakeStatusUpdater) UpdateStatus(ctx context.Context, id, status string) error {
	f.mu.Lock()
	f.statuses = append(f.statuses, status)
	f.mu.Unlock()
	return nil
}

func (f *fakeStatusUpdater) SaveValidation(ctx context.Context, id, verdict string, report []byte) error {
	f.mu.Lock()
	f.verdict, f.report = verdict, report
	f.mu.Unlock()
	return nil
}

func (f *fakeStatusUpdater) ClearValidation(ctx context.Context, id string) error {
	f.mu.Lock()
	f.clearedN++
	f.verdict, f.report = "", nil
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

// A published bundle carries its verdict on the file record and inside its
// own log.md, so the user can see not just that it worked but what was
// checked.
func TestConvertRecordsTheValidationVerdict(t *testing.T) {
	const src = "user-1/src.md"
	h := newHarness(map[string]string{src: "# Uno\n\na\n\n# Dos\n\nb"})

	job := Job{JobID: "job-1", FileID: "file-1", ObjectKey: src, ContentType: "text/markdown", OriginalName: "notas.md"}
	if err := h.conv.Convert(context.Background(), job); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if h.statuses.verdict != string(bundle.VerdictValid) {
		t.Errorf("recorded verdict = %q, want %q", h.statuses.verdict, bundle.VerdictValid)
	}

	var report bundle.Report
	if err := json.Unmarshal(h.statuses.report, &report); err != nil {
		t.Fatalf("stored report is not valid JSON: %v", err)
	}
	if len(report.Checks) == 0 {
		t.Error("stored report lists no checks")
	}

	// The previous attempt's verdict is dropped before a new one starts, so a
	// retry in flight never shows the result it is replacing.
	if h.statuses.clearedN != 1 {
		t.Errorf("ClearValidation called %d times, want 1", h.statuses.clearedN)
	}

	log, _ := h.bundleFile(src, bundle.LogFile)
	if !strings.Contains(log, "## Validación") {
		t.Errorf("log.md does not carry the validation section:\n%s", log)
	}
}

// §6: a bundle that fails validation is not published - nothing reaches
// object storage, no output rows are recorded, no download is possible - but
// the verdict is still on record so the user can be told why.
func TestConvertRefusesToPublishAnInvalidBundle(t *testing.T) {
	const src = "user-1/src.md"
	h := newHarness(map[string]string{src: "# Uno\n\na\n\n# Dos\n\nb"})

	h.conv.validator = func(b bundle.Bundle) bundle.Report {
		return bundle.Report{
			Verdict:  bundle.VerdictInvalid,
			Platform: bundle.VerdictInvalid,
			OKF:      bundle.VerdictValid,
			Checks: []bundle.Check{{
				ID:       "minimum-structure",
				Scope:    bundle.ScopePlatform,
				Rule:     "El bundle contiene index.md, log.md y al menos un concepto",
				Severity: bundle.SeverityError,
				Details:  []string{"falta `log.md`"},
			}},
		}
	}

	job := Job{JobID: "job-1", FileID: "file-1", ObjectKey: src, ContentType: "text/markdown", OriginalName: "notas.md"}
	err := h.conv.Convert(context.Background(), job)
	if err == nil {
		t.Fatal("expected an error when the bundle fails validation, got nil")
	}
	if !strings.Contains(err.Error(), "no superó la validación") {
		t.Errorf("error = %q, want it to name validation as the cause", err)
	}

	if len(h.store.objects) != 1 {
		t.Errorf("stored %d objects, want only the untouched source document", len(h.store.objects))
	}
	if _, ok := h.store.objects[storage.ResultObjectName(src)]; ok {
		t.Error("an invalid bundle was packaged and stored anyway")
	}
	if names := h.outputs.names(); len(names) != 0 {
		t.Errorf("recorded %v as downloadable outputs of an invalid bundle", names)
	}

	if h.statuses.verdict != string(bundle.VerdictInvalid) {
		t.Errorf("recorded verdict = %q, want %q", h.statuses.verdict, bundle.VerdictInvalid)
	}

	wantStatuses := []string{StatusConverting, StatusFailed}
	if !equalStrings(h.statuses.statuses, wantStatuses) {
		t.Errorf("statuses = %v, want %v", h.statuses.statuses, wantStatuses)
	}
}

// Warnings are not a reason to withhold a bundle: it is published, and the
// classification records that it was not spotless.
func TestConvertPublishesABundleWithWarnings(t *testing.T) {
	const src = "user-1/src.md"
	h := newHarness(map[string]string{src: "# Uno\n\nver [el anexo](anexo-b.md)"})

	job := Job{JobID: "job-1", FileID: "file-1", ObjectKey: src, ContentType: "text/markdown", OriginalName: "notas.md"}
	if err := h.conv.Convert(context.Background(), job); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	if h.statuses.verdict != string(bundle.VerdictWithWarnings) {
		t.Errorf("recorded verdict = %q, want %q", h.statuses.verdict, bundle.VerdictWithWarnings)
	}
	if _, ok := h.store.objects[storage.ResultObjectName(src)]; !ok {
		t.Error("a bundle with only warnings was not published")
	}

	wantStatuses := []string{StatusConverting, StatusConverted}
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
