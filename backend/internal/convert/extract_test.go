package convert

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		contentType string
		filename    string
		want        sourceFormat
	}{
		{"text/plain", "notes.bin", formatText},
		{"text/plain; charset=utf-8", "notes.bin", formatText},
		{"text/csv", "data.bin", formatCSV},
		{"application/pdf", "doc.bin", formatPDF},
		{"application/octet-stream", "notes.txt", formatText},
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

func TestExtractText(t *testing.T) {
	got, err := extractText(strings.NewReader("alpha\n\nbeta"))
	if err != nil {
		t.Fatalf("extractText() error = %v", err)
	}

	want := []string{"alpha", "beta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractText() = %#v, want %#v", got, want)
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

func TestExtractParagraphsUnsupportedFormat(t *testing.T) {
	_, err := extractParagraphs("application/zip", "archive.zip", strings.NewReader("junk"))
	if err == nil {
		t.Fatal("expected error for unsupported content type, got nil")
	}
}

func TestExtractPDFInvalidData(t *testing.T) {
	_, err := extractPDF(strings.NewReader("not a real pdf"))
	if err == nil {
		t.Fatal("expected error for invalid PDF data, got nil")
	}
}
