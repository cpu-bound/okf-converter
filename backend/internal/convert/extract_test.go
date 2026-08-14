package convert

import (
	"reflect"
	"strings"
	"testing"
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
