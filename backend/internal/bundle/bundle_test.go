package bundle

import (
	"archive/zip"
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testSource() Source {
	return Source{
		OriginalName: "notas-de-clase.txt",
		ContentType:  "text/plain",
		Size:         2048,
		FileID:       "file-1",
		JobID:        "job-1",
		ConvertedAt:  time.Date(2026, 8, 15, 18, 42, 0, 0, time.UTC),
	}
}

func build(t *testing.T, units []Unit) Bundle {
	t.Helper()

	b, err := Build(testSource(), units, NewLog())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	return b
}

func TestBuildRequiresAtLeastOneUnit(t *testing.T) {
	if _, err := Build(testSource(), nil, NewLog()); err == nil {
		t.Fatal("expected an error when there are no units, got nil")
	}
}

// The minimum structure is mandatory even for a short document with no
// divisions - and producing a single concept is a normal outcome, not a
// warning.
func TestBuildSingleUnitProducesMinimumStructure(t *testing.T) {
	b := build(t, []Unit{{Title: "Resumen", Body: "una sola idea"}})

	if got := b.ConceptCount(); got != 1 {
		t.Errorf("ConceptCount() = %d, want 1", got)
	}

	for _, name := range []string{IndexFile, LogFile} {
		content, ok := b.Find(name)
		if !ok {
			t.Fatalf("bundle is missing %s", name)
		}
		if len(content) == 0 {
			t.Errorf("%s is empty", name)
		}
	}

	if len(b.Files) != 3 {
		t.Errorf("bundle holds %d files, want 3 (index.md, log.md, one concept)", len(b.Files))
	}
}

// linkRE matches the concept links in index.md's ordered list.
var linkRE = regexp.MustCompile(`\d+\. \[[^\]]*\]\(([^)]+)\)`)

func TestIndexLinksEveryConceptInOrder(t *testing.T) {
	b := build(t, []Unit{
		{Title: "Introducción", Body: "a"},
		{Title: "Metodología", Body: "b"},
		{Title: "Conclusiones", Body: "c"},
	})

	index, ok := b.Find(IndexFile)
	if !ok {
		t.Fatal("bundle is missing index.md")
	}

	matches := linkRE.FindAllStringSubmatch(string(index), -1)
	if len(matches) != 3 {
		t.Fatalf("index.md holds %d concept links, want 3:\n%s", len(matches), index)
	}

	// Every link must resolve to a file that is actually in the bundle -
	// the property the bundle validation later enforces before publishing.
	for i, m := range matches {
		target := m[1]
		if _, ok := b.Find(target); !ok {
			t.Errorf("index.md link %d points at %q, which is not in the bundle", i+1, target)
		}
	}

	// And they must appear in the source document's order.
	want := []string{"01-introduccion.md", "02-metodologia.md", "03-conclusiones.md"}
	for i, m := range matches {
		if m[1] != want[i] {
			t.Errorf("link %d = %q, want %q", i+1, m[1], want[i])
		}
	}
}

func TestBuildDerivesTitlesForUntitledUnits(t *testing.T) {
	b := build(t, []Unit{{Body: "sin título"}, {Body: "tampoco"}})

	index, _ := b.Find(IndexFile)
	for _, want := range []string{"Sección 1", "Sección 2"} {
		if !strings.Contains(string(index), want) {
			t.Errorf("index.md does not name the untitled unit %q:\n%s", want, index)
		}
	}

	for _, want := range []string{"01-seccion-1.md", "02-seccion-2.md"} {
		if _, ok := b.Find(want); !ok {
			t.Errorf("bundle is missing %q", want)
		}
	}
}

// Two sections sharing a heading is ordinary in a real document; the second
// one has to get its own file rather than overwrite the first.
func TestBuildResolvesDuplicateTitles(t *testing.T) {
	b := build(t, []Unit{
		{Title: "Ejemplo", Body: "primero"},
		{Title: "Ejemplo", Body: "segundo"},
	})

	if b.ConceptCount() != 2 {
		t.Fatalf("ConceptCount() = %d, want 2", b.ConceptCount())
	}

	seen := map[string]bool{}
	for _, f := range b.Files {
		if seen[f.Name] {
			t.Fatalf("bundle holds two files named %q", f.Name)
		}
		seen[f.Name] = true
	}

	first, ok := b.Find("01-ejemplo.md")
	if !ok {
		t.Fatal("bundle is missing 01-ejemplo.md")
	}
	if !strings.Contains(string(first), "primero") {
		t.Errorf("01-ejemplo.md holds the wrong body:\n%s", first)
	}
}

func TestConceptCarriesTitleBodyAndNavigation(t *testing.T) {
	b := build(t, []Unit{
		{Title: "Uno", Body: "cuerpo uno"},
		{Title: "Dos", Body: "cuerpo dos"},
	})

	second, ok := b.Find("02-dos.md")
	if !ok {
		t.Fatal("bundle is missing 02-dos.md")
	}

	content := string(second)
	if !strings.HasPrefix(content, "# Dos\n") {
		t.Errorf("concept does not open with its title:\n%s", content)
	}
	if !strings.Contains(content, "cuerpo dos") {
		t.Errorf("concept does not carry its body:\n%s", content)
	}
	if !strings.Contains(content, "("+IndexFile+")") {
		t.Errorf("concept does not link back to the index:\n%s", content)
	}
	if !strings.Contains(content, "(01-uno.md)") {
		t.Errorf("concept does not link to the previous one:\n%s", content)
	}
}

func TestLogRecordsStepsAndUnits(t *testing.T) {
	log := newLogAt(func() time.Time { return time.Date(2026, 8, 15, 18, 42, 3, 0, time.UTC) })
	log.Step("documento descargado desde %s", "MinIO")

	b, err := Build(testSource(), []Unit{{Title: "Uno", Body: "x", Origin: "encabezado nivel 1"}}, log)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	content, ok := b.Find(LogFile)
	if !ok {
		t.Fatal("bundle is missing log.md")
	}

	for _, want := range []string{
		"documento descargado desde MinIO", // caller's step
		"18:42:03.000",                     // when it happened
		"job-1",                            // which attempt produced this
		"Unidades detectadas",
		"encabezado nivel 1", // where the unit came from
		"01-uno.md",
	} {
		if !strings.Contains(string(content), want) {
			t.Errorf("log.md does not mention %q:\n%s", want, content)
		}
	}
}

// Validation results are only known once the bundle exists, so log.md has to
// be able to pick up entries added after Build.
func TestRefreshLogPicksUpLaterEntries(t *testing.T) {
	b := build(t, []Unit{{Title: "Uno", Body: "x"}})

	b.Log().Step("validación superada: estructura mínima")
	b.RefreshLog()

	content, _ := b.Find(LogFile)
	if !strings.Contains(string(content), "validación superada: estructura mínima") {
		t.Errorf("log.md was not refreshed:\n%s", content)
	}

	if len(b.Files) != 3 {
		t.Errorf("RefreshLog changed the file count to %d, want 3", len(b.Files))
	}
}

func TestZipNestsEverythingUnderTheBundleRoot(t *testing.T) {
	b := build(t, []Unit{{Title: "Uno", Body: "x"}, {Title: "Dos", Body: "y"}})

	archive, err := b.Zip()
	if err != nil {
		t.Fatalf("Zip() error = %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("Zip() produced an invalid archive: %v", err)
	}

	if len(zr.File) != len(b.Files) {
		t.Fatalf("archive holds %d entries, want %d", len(zr.File), len(b.Files))
	}

	want := []string{
		b.Root + "/" + IndexFile,
		b.Root + "/" + LogFile,
		b.Root + "/01-uno.md",
		b.Root + "/02-dos.md",
	}
	for i, f := range zr.File {
		if f.Name != want[i] {
			t.Errorf("archive entry %d = %q, want %q", i, f.Name, want[i])
		}
	}
}

// Re-converting the same document has to produce the same bytes, so a
// redelivered job overwrites its result with an identical archive instead of
// a gratuitously different one.
func TestZipIsDeterministic(t *testing.T) {
	units := []Unit{{Title: "Uno", Body: "x"}}

	log := newLogAt(func() time.Time { return time.Date(2026, 8, 15, 18, 42, 0, 0, time.UTC) })
	first, err := Build(testSource(), units, log)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	log = newLogAt(func() time.Time { return time.Date(2026, 8, 15, 18, 42, 0, 0, time.UTC) })
	second, err := Build(testSource(), units, log)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	a, err := first.Zip()
	if err != nil {
		t.Fatalf("Zip() error = %v", err)
	}
	c, err := second.Zip()
	if err != nil {
		t.Fatalf("Zip() error = %v", err)
	}

	if !bytes.Equal(a, c) {
		t.Error("two builds of the same document produced different archives")
	}
}

func TestDocumentTitle(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"notas-de-clase.txt", "Notas de clase"},
		{"informe_final.pdf", "Informe final"},
		{"README.md", "README"},
		{"sin-extension", "Sin extension"},
		{"", "Documento"},
	}

	for _, tt := range tests {
		if got := documentTitle(tt.name); got != tt.want {
			t.Errorf("documentTitle(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
