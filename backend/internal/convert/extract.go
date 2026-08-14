package convert

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/ledongthuc/pdf"
)

// blankLineRE splits text into paragraphs on one or more blank lines.
var blankLineRE = regexp.MustCompile(`\r?\n[ \t]*\r?\n+`)

type sourceFormat int

const (
	formatUnknown sourceFormat = iota
	formatText
	formatCSV
	formatPDF
)

// detectFormat identifies the source format from the declared content type,
// falling back to the filename extension since browsers/clients don't
// always send a precise Content-Type for text-ish uploads.
func detectFormat(contentType, filename string) sourceFormat {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch ct {
	case "text/plain":
		return formatText
	case "text/csv", "application/csv", "application/vnd.ms-excel":
		return formatCSV
	case "application/pdf":
		return formatPDF
	}

	name := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(name, ".txt"):
		return formatText
	case strings.HasSuffix(name, ".csv"):
		return formatCSV
	case strings.HasSuffix(name, ".pdf"):
		return formatPDF
	}

	return formatUnknown
}

// extractParagraphs reads r and splits its text content into chunks - one
// output file per chunk. What counts as a "paragraph" depends on the source
// format: blank-line-delimited blocks for text/PDF, one chunk per row for
// CSV.
func extractParagraphs(contentType, filename string, r io.Reader) ([]string, error) {
	switch detectFormat(contentType, filename) {
	case formatText:
		return extractText(r)
	case formatCSV:
		return extractCSV(r)
	case formatPDF:
		return extractPDF(r)
	default:
		return nil, fmt.Errorf("unsupported content type %q", contentType)
	}
}

func extractText(r io.Reader) ([]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read text: %w", err)
	}
	return splitParagraphs(string(data)), nil
}

func splitParagraphs(s string) []string {
	parts := blankLineRE.Split(s, -1)

	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// extractCSV treats each row as its own chunk, joining fields with ", "
// into a single line of text.
func extractCSV(r io.Reader) ([]string, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1

	var out []string
	for {
		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read csv: %w", err)
		}

		line := strings.TrimSpace(strings.Join(record, ", "))
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// extractPDF reads the whole file into memory (pdf.NewReader needs an
// io.ReaderAt) and pulls the document's plain text, then splits it the same
// way as a text file. Uploads are already capped at 25MB, so buffering the
// whole thing is fine.
func extractPDF(r io.Reader) ([]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read pdf: %w", err)
	}

	doc, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open pdf: %w", err)
	}

	textReader, err := doc.GetPlainText()
	if err != nil {
		return nil, fmt.Errorf("extract pdf text: %w", err)
	}

	text, err := io.ReadAll(textReader)
	if err != nil {
		return nil, fmt.Errorf("read pdf text: %w", err)
	}

	return splitParagraphs(string(text)), nil
}
