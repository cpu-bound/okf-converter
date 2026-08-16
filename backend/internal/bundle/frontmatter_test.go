package bundle

import (
	"strings"
	"testing"
	"time"
)

// frontmatterFields pulls the YAML block off the front of a bundle file and
// returns its values unquoted, so tests can assert on the fields OKF
// consumers query rather than on exact formatting.
//
// It goes through the same parser the validator uses, so a file the tests can
// read is by construction a file validation can read.
func frontmatterFields(t *testing.T, content []byte) map[string]string {
	t.Helper()

	raw, ok := parseFrontmatter(content)
	if !ok {
		t.Fatalf("file does not open with a YAML frontmatter block:\n%s", content)
	}

	fields := make(map[string]string, len(raw))
	for key, value := range raw {
		fields[key] = unquoteYAML(value)
	}
	return fields
}

// OKF requires exactly one field of every concept: type. Every file in the
// bundle carries one, including the two reserved filenames.
func TestEveryFileCarriesAnOKFType(t *testing.T) {
	b := build(t, []Unit{{Title: "Uno", Body: "x"}, {Title: "Dos", Body: "y"}})

	wantTypes := map[string]string{
		IndexFile:   TypeIndex,
		LogFile:     TypeLog,
		"01-uno.md": TypeConcept,
		"02-dos.md": TypeConcept,
	}

	if len(b.Files) != len(wantTypes) {
		t.Fatalf("bundle holds %d files, want %d", len(b.Files), len(wantTypes))
	}

	for _, f := range b.Files {
		fields := frontmatterFields(t, f.Content)

		want, known := wantTypes[f.Name]
		if !known {
			t.Errorf("unexpected file %q in the bundle", f.Name)
			continue
		}
		if fields["type"] != want {
			t.Errorf("%s type = %q, want %q", f.Name, fields["type"], want)
		}
	}
}

func TestConceptFrontmatterCarriesTheQueryableFields(t *testing.T) {
	b := build(t, []Unit{{Title: "Introducción", Body: "x"}, {Title: "Cierre", Body: "y"}})

	content, ok := b.Find("01-introduccion.md")
	if !ok {
		t.Fatal("bundle is missing 01-introduccion.md")
	}

	fields := frontmatterFields(t, content)

	if fields["title"] != "Introducción" {
		t.Errorf("title = %q, want %q", fields["title"], "Introducción")
	}
	if !strings.Contains(fields["description"], "Unidad 1 de 2") {
		t.Errorf("description = %q, want it to place the unit in the document", fields["description"])
	}
	if fields["source"] != "notas-de-clase.txt" {
		t.Errorf("source = %q, want the original document", fields["source"])
	}
	if _, err := time.Parse(time.RFC3339, fields["timestamp"]); err != nil {
		t.Errorf("timestamp = %q, want RFC3339: %v", fields["timestamp"], err)
	}
	if !strings.Contains(fields["tags"], "okf") {
		t.Errorf("tags = %q, want them to include okf", fields["tags"])
	}
}

// A title containing YAML syntax must survive as a plain string rather than
// turning the frontmatter into something else.
func TestFrontmatterQuotesTroublesomeTitles(t *testing.T) {
	b := build(t, []Unit{{Title: `Análisis: "costos" & márgenes`, Body: "x"}})

	var content []byte
	for _, f := range b.Files {
		if f.Name != IndexFile && f.Name != LogFile {
			content = f.Content
		}
	}
	if content == nil {
		t.Fatal("bundle holds no concept file")
	}

	if got := frontmatterFields(t, content)["title"]; !strings.Contains(got, "costos") {
		t.Errorf("title = %q, want the original title preserved", got)
	}
	if !strings.Contains(string(content), `title: "Análisis: \"costos\" & márgenes"`) {
		t.Errorf("title was not quoted and escaped:\n%s", content)
	}
}

func TestYamlString(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"simple", `"simple"`},
		{"con: dos puntos", `"con: dos puntos"`},
		{`con "comillas"`, `"con \"comillas\""`},
		{"con\nsalto", `"con salto"`},
		{`con\barra`, `"con\\barra"`},
	}

	for _, tt := range tests {
		if got := yamlString(tt.in); got != tt.want {
			t.Errorf("yamlString(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}
