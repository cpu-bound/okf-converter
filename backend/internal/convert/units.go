package convert

import (
	"fmt"
	"io"
	"strings"

	"okf-converter/backend/internal/bundle"
)

// maxTitleRunes caps a derived title's length. Titles derived from a body of
// text are frequently a whole sentence; the file only needs a recognizable
// name, and the full text is right there inside the document anyway.
const maxTitleRunes = 70

// extractUnits turns the source document into the ordered logical units the
// bundle is built from, recording what it found into log so log.md can show
// how the document was read.
//
// Unit detection currently reuses the format-specific block splitting in
// extract.go (blank-line paragraphs for text and PDF, one row per record for
// CSV) and derives each unit's title from its own first line. Splitting by
// document structure - headings - is the next step and belongs here; nothing
// downstream needs to change when it lands, since everything past this
// function only sees []bundle.Unit.
func extractUnits(contentType, filename string, r io.Reader, log *bundle.Log) ([]bundle.Unit, error) {
	format := detectFormat(contentType, filename)
	log.Step("formato de origen detectado: %s", formatName(format))

	blocks, err := extractParagraphs(contentType, filename, r)
	if err != nil {
		return nil, err
	}

	if len(blocks) == 0 {
		return nil, fmt.Errorf("no extractable text content")
	}

	units := make([]bundle.Unit, 0, len(blocks))
	for i, block := range blocks {
		units = append(units, bundle.Unit{
			Title:  titleFromBody(block),
			Body:   block,
			Origin: fmt.Sprintf("bloque %d de %d", i+1, len(blocks)),
		})
	}

	log.Step("%d unidad(es) lógica(s) detectada(s) en el documento", len(units))
	return units, nil
}

// titleFromBody derives a heading for a unit that doesn't carry one, from
// its own first line. It returns "" when nothing usable is left, leaving the
// fallback to bundle.Build - which numbers the unit by position, the one
// naming that always works.
func titleFromBody(body string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")

	// Strip the markers that would otherwise end up inside the title: an
	// existing Markdown heading, a list bullet, a block quote.
	line = strings.TrimLeft(line, "#>*-+ \t")
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}

	line = truncateRunes(line, maxTitleRunes)

	// A title reads as a label, not as a sentence, so trailing sentence
	// punctuation goes - but a trailing '?' or '!' carries meaning and stays.
	return strings.TrimRight(line, ".,;: \t")
}

// truncateRunes cuts s to at most n runes, preferring the last word boundary
// so a truncated title doesn't end mid-word, and marking the cut with an
// ellipsis.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}

	cut := string(runes[:n])
	if idx := strings.LastIndexAny(cut, " \t"); idx > n/2 {
		cut = cut[:idx]
	}

	return strings.TrimRight(cut, " \t") + "…"
}

func formatName(f sourceFormat) string {
	switch f {
	case formatText:
		return "texto plano"
	case formatCSV:
		return "CSV"
	case formatPDF:
		return "PDF"
	default:
		return "desconocido"
	}
}
