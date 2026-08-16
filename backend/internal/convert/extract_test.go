package convert

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"okf-converter/backend/internal/bundle"
)

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		contentType string
		filename    string
		want        sourceFormat
	}{
		{"text/plain", "notes.bin", formatText},
		{"text/plain; charset=utf-8", "notes.bin", formatText},
		{"text/markdown", "notes.bin", formatMarkdown},
		{"text/html", "page.bin", formatHTML},
		{"text/csv", "data.bin", formatCSV},
		{"application/pdf", "doc.bin", formatPDF},
		{"application/octet-stream", "notes.txt", formatText},
		{"application/octet-stream", "notes.MD", formatMarkdown},
		{"application/octet-stream", "page.html", formatHTML},
		{"application/octet-stream", "page.htm", formatHTML},
		{"application/octet-stream", "data.CSV", formatCSV},
		{"application/octet-stream", "doc.pdf", formatPDF},
		{"application/octet-stream", "unknown.bin", formatUnknown},
	}

	for _, tt := range tests {
		if got := detectFormat(tt.contentType, tt.filename); got != tt.want {
			t.Errorf("detectFormat(%q, %q) = %v, want %v", tt.contentType, tt.filename, got, tt.want)
		}
	}
}

func TestSupports(t *testing.T) {
	if !Supports("text/markdown", "notes.md") {
		t.Error("Supports() = false for Markdown, want true")
	}
	if Supports("application/zip", "archive.zip") {
		t.Error("Supports() = true for a zip archive, want false")
	}
}

func TestSplitParagraphs(t *testing.T) {
	input := "First paragraph.\nStill first.\n\nSecond paragraph.\n\n\n\nThird paragraph.\n   \n"
	want := []string{"First paragraph.\nStill first.", "Second paragraph.", "Third paragraph."}

	got := splitParagraphs(input)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitParagraphs() = %#v, want %#v", got, want)
	}
}

func TestSplitParagraphsFallsBackToFixedSizeChunksForOversizedBlocks(t *testing.T) {
	// A single block with no blank lines at all - like the near-space-less,
	// near-paragraph-break-less output some PDF extractions produce - well
	// over the maxParagraphBytes fallback threshold.
	block := strings.Repeat("a", maxParagraphBytes*2+500)

	got := splitParagraphs(block)

	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3 (two full-size plus a remainder)", len(got))
	}

	for i, want := range []int{maxParagraphBytes, maxParagraphBytes, 500} {
		if len(got[i]) != want {
			t.Errorf("chunk %d length = %d, want %d", i, len(got[i]), want)
		}
	}

	rejoined := strings.Join(got, "")
	if rejoined != block {
		t.Error("splitParagraphs() lost or reordered content when splitting an oversized block")
	}
}

func TestSplitOversizedBlockPreservesUTF8Boundaries(t *testing.T) {
	// A multi-byte rune ("é", 2 bytes in UTF-8) placed to straddle the cut
	// point, so a naive byte-count split would slice it in half.
	s := strings.Repeat("a", maxParagraphBytes-1) + "é" + strings.Repeat("b", 100)

	got := splitOversizedBlock(s)

	for i, chunk := range got {
		if !utf8.ValidString(chunk) {
			t.Errorf("chunk %d is not valid UTF-8: %q", i, chunk)
		}
	}

	if strings.Join(got, "") != s {
		t.Error("splitOversizedBlock() lost or reordered content")
	}
}

func TestExtractCSV(t *testing.T) {
	got, err := extractCSV(strings.NewReader("name,age\nAda,36\nGrace,85\n"))
	if err != nil {
		t.Fatalf("extractCSV() error = %v", err)
	}

	want := []string{"name, age", "Ada, 36", "Grace, 85"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractCSV() = %#v, want %#v", got, want)
	}
}

func TestExtractUnitsUnsupportedFormat(t *testing.T) {
	_, err := extractUnits("application/zip", "archive.zip", strings.NewReader("junk"), bundle.NewLog())
	if err == nil {
		t.Fatal("expected error for unsupported content type, got nil")
	}
}

func TestExtractPDFInvalidData(t *testing.T) {
	_, err := extractPDFText(strings.NewReader("not a real pdf"))
	if err == nil {
		t.Fatal("expected error for invalid PDF data, got nil")
	}
}

func TestHTMLToText(t *testing.T) {
	const page = `<!doctype html>
<html>
<head><title>Ignorado</title><style>body { color: red }</style></head>
<body>
  <h1>Guía de OKF</h1>
  <p>Un bundle es una <strong>carpeta</strong> autocontenida.</p>
  <h2>Conceptos</h2>
  <p>Cada unidad lógica es un documento.</p>
  <script>console.log("ignorado")</script>
</body>
</html>`

	got, err := htmlToText(strings.NewReader(page))
	if err != nil {
		t.Fatalf("htmlToText() error = %v", err)
	}

	// Headings become ATX so the Markdown segmenter can see the structure.
	for _, want := range []string{"# Guía de OKF", "## Conceptos"} {
		if !strings.Contains(got, want) {
			t.Errorf("htmlToText() lost the heading %q:\n%s", want, got)
		}
	}

	// Inline markup is unwrapped, but its text survives as one phrase.
	if !strings.Contains(got, "Un bundle es una carpeta autocontenida.") {
		t.Errorf("htmlToText() mangled the paragraph text:\n%s", got)
	}

	// Machinery is dropped entirely, including the <head>.
	for _, unwanted := range []string{"console.log", "color: red", "Ignorado"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("htmlToText() kept %q, which is not document text:\n%s", unwanted, got)
		}
	}
}
