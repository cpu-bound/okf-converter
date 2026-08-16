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
// bundle is built from, recording what it found into log so log.md shows how
// the document was read.
//
// Each format is reduced to something the segmenter understands rather than
// getting its own splitting rules: Markdown is already there, HTML is
// rendered as Markdown-ish text (its <h1>-<h6> become headings), and PDF
// text is treated as plain text. Only CSV is different, because its logical
// units are its rows and no amount of heading detection would find them.
func extractUnits(contentType, filename string, r io.Reader, log *bundle.Log) ([]bundle.Unit, error) {
	format := detectFormat(contentType, filename)
	log.Step("formato de origen detectado: %s", formatName(format))

	var (
		text  string
		style headingStyle
		err   error
	)

	switch format {
	case formatMarkdown:
		text, err = readText(r)
		style = styleMarkup

	case formatHTML:
		text, err = htmlToText(r)
		style = styleMarkup

	case formatText:
		text, err = readText(r)
		style = stylePlain

	case formatPDF:
		text, err = extractPDFText(r)
		style = stylePlain

	case formatCSV:
		return csvUnits(r, log)

	default:
		return nil, fmt.Errorf("unsupported content type %q", contentType)
	}

	if err != nil {
		return nil, err
	}

	units := segmentText(text, style, log)
	if len(units) == 0 {
		return nil, fmt.Errorf("no extractable text content")
	}

	log.Step("%d unidad(es) lógica(s) detectada(s) en el documento", len(units))
	return units, nil
}

// csvUnits makes one unit per row. The header row, when there is one, is not
// treated specially: it is the first row of the document and stays in that
// position, which keeps the bundle's order faithful to the source.
func csvUnits(r io.Reader, log *bundle.Log) ([]bundle.Unit, error) {
	rows, err := extractCSV(r)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no extractable text content")
	}

	log.Step("estructura tabular: cada fila del CSV es una unidad")

	units := make([]bundle.Unit, 0, len(rows))
	for i, row := range rows {
		units = append(units, bundle.Unit{
			Title:  titleFromBody(row),
			Body:   row,
			Origin: fmt.Sprintf("fila %d de %d", i+1, len(rows)),
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
	if idx := strings.LastIndexAny(cut, " \t"); idx > len(cut)/2 {
		cut = cut[:idx]
	}

	return strings.TrimRight(cut, " \t") + "…"
}

func formatName(f sourceFormat) string {
	switch f {
	case formatText:
		return "texto plano"
	case formatMarkdown:
		return "Markdown"
	case formatHTML:
		return "HTML"
	case formatCSV:
		return "CSV"
	case formatPDF:
		return "PDF"
	default:
		return "desconocido"
	}
}
