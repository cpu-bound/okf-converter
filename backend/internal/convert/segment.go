package convert

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"okf-converter/backend/internal/bundle"
)

// maxUnstructuredBytes is how much text the segmenter is willing to leave as
// a single unit when it finds no structure at all. Below it, a document with
// no headings is exactly what it looks like - one short note - and turning
// it into one concept is the right answer, which is also what the brief
// requires of a "documento breve" (§6). Above it, an unstructured document
// is more likely a wall of text whose only usable boundaries are its
// paragraphs, and one enormous concept would help nobody.
const maxUnstructuredBytes = maxParagraphBytes

// headingStyle selects which heading conventions the segmenter looks for.
type headingStyle int

const (
	// styleMarkup is for documents whose headings are explicit: Markdown's
	// '#' and setext underlines, or the ATX headings htmlToText renders
	// <h1>-<h6> as. Nothing is guessed, because nothing needs to be.
	styleMarkup headingStyle = iota

	// stylePlain is for plain text and extracted PDF text, where headings
	// are a convention rather than syntax. It accepts the markup forms too -
	// people do write '#' headings in .txt files - plus numbered sections
	// ("3.1 Metodología") and the usual section words ("Capítulo IV").
	stylePlain
)

var (
	// atxRE matches a Markdown ATX heading: up to three leading spaces, one
	// to six '#', a space, and the title (with optional closing '#'s).
	atxRE = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(\S.*?)[ \t]*#*[ \t]*$`)

	// setextRE matches the underline of a setext heading: a run of '=' (h1)
	// or '-' (h2) on its own line.
	setextRE = regexp.MustCompile(`^ {0,3}(=+|-{3,})[ \t]*$`)

	// fenceRE matches the start or end of a fenced code block, whose
	// contents must never be read as headings.
	fenceRE = regexp.MustCompile("^ {0,3}(```|~~~)")

	// multiLevelRE matches a multi-level numbered heading ("3.1", "3.1.2"),
	// where the numbering itself is unambiguous enough not to need a
	// separator.
	multiLevelRE = regexp.MustCompile(`^[ \t]*(\d+(?:\.\d+)+)[.)]?[ \t]+(\S.*)$`)

	// singleLevelRE matches a single-number heading, which *does* need a
	// separator: "1. Introducción" is a heading, but "2024 fue un año
	// difícil" is a sentence, and only the punctuation tells them apart.
	singleLevelRE = regexp.MustCompile(`^[ \t]*(\d+)[.)][ \t]+(\S.*)$`)

	// keywordRE matches the words documents use to open a section, followed
	// by an arabic or roman numeral.
	keywordRE = regexp.MustCompile(`(?i)^[ \t]*(cap[íi]tulo|secci[óo]n|parte|anexo|ap[ée]ndice|chapter|section|part)[ \t]+([0-9]+|[ivxlcIVXLC]+)\b`)
)

// maxHeadingRunes bounds how long a line can be and still be read as a
// heading in a plain-text document. Headings are labels; a numbered
// paragraph that runs on for a full sentence is a paragraph.
const maxHeadingRunes = 90

type heading struct {
	level int
	title string
	line  int // 0-based index into the document's lines
}

// segmentText splits a text document into the logical units the bundle is
// built from, using its own structure: one unit per top-level section.
//
// "Top level" means the shallowest heading level that actually divides the
// document - a paper whose only level-1 heading is its title, followed by
// level-2 sections, is divided by those sections rather than left as one
// unit. When the document has no headings at all, it becomes a single unit
// (a short note is one concept, not zero and not an arbitrary number), and
// only an unstructured document too large for that falls back to splitting
// on paragraphs.
func segmentText(text string, style headingStyle, log *bundle.Log) []bundle.Unit {
	text = strings.ReplaceAll(text, "\r\n", "\n")

	lines := strings.Split(text, "\n")
	headings := detectHeadings(lines, style)

	if len(headings) == 0 {
		return segmentWithoutHeadings(text, log)
	}

	level := splitLevel(headings)
	log.Step("estructura detectada: %d encabezado(s), se segmenta por el nivel %d", len(headings), level)

	var splits []heading
	for _, h := range headings {
		if h.level == level {
			splits = append(splits, h)
		}
	}

	var units []bundle.Unit

	// Anything before the first section heading is the document's preamble -
	// its title page, abstract or introduction. It is only a unit of its own
	// if it actually carries content; a lone document title is not a
	// concept.
	if preamble := strings.TrimSpace(strings.Join(lines[:splits[0].line], "\n")); preamble != "" {
		if title, body := splitLeadingHeading(preamble); body != "" {
			units = append(units, bundle.Unit{
				Title:  title,
				Body:   body,
				Origin: fmt.Sprintf("preámbulo, líneas 1–%d", splits[0].line),
			})
		}
	}

	for i, h := range splits {
		end := len(lines)
		if i+1 < len(splits) {
			end = splits[i+1].line
		}

		units = append(units, bundle.Unit{
			Title:  h.title,
			Body:   strings.TrimSpace(strings.Join(lines[h.line+1:end], "\n")),
			Origin: fmt.Sprintf("encabezado de nivel %d, línea %d", h.level, h.line+1),
		})
	}

	return units
}

// segmentWithoutHeadings handles a document whose structure gives the
// segmenter nothing to work with.
func segmentWithoutHeadings(text string, log *bundle.Log) []bundle.Unit {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}

	if len(trimmed) <= maxUnstructuredBytes {
		log.Step("sin encabezados y documento breve: se genera un único concepto")
		return []bundle.Unit{{
			Title:  titleFromBody(trimmed),
			Body:   trimmed,
			Origin: "documento completo, sin estructura detectada",
		}}
	}

	blocks := splitParagraphs(trimmed)
	log.Step("sin encabezados en un documento extenso: se segmenta en %d bloque(s) por párrafo", len(blocks))

	units := make([]bundle.Unit, 0, len(blocks))
	for i, block := range blocks {
		units = append(units, bundle.Unit{
			Title:  titleFromBody(block),
			Body:   block,
			Origin: fmt.Sprintf("bloque %d de %d, sin estructura detectada", i+1, len(blocks)),
		})
	}
	return units
}

// detectHeadings walks the document once and returns every heading it
// recognizes, in order.
func detectHeadings(lines []string, style headingStyle) []heading {
	var headings []heading
	inFence := false

	for i, line := range lines {
		if fenceRE.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence || strings.TrimSpace(line) == "" {
			continue
		}

		if m := atxRE.FindStringSubmatch(line); m != nil {
			headings = append(headings, heading{level: len(m[1]), title: strings.TrimSpace(m[2]), line: i})
			continue
		}

		// A setext underline turns the line above it into a heading. It is
		// recognized from the underline (rather than by looking ahead) so
		// the '---' of a thematic break, which has no text above it, is
		// ignored.
		if i > 0 && setextRE.MatchString(line) {
			above := strings.TrimSpace(lines[i-1])
			alreadyRecorded := len(headings) > 0 && headings[len(headings)-1].line == i-1

			if above == "" || isUnderline(above) || alreadyRecorded {
				continue
			}

			level := 2
			if strings.HasPrefix(strings.TrimSpace(line), "=") {
				level = 1
			}
			headings = append(headings, heading{level: level, title: above, line: i - 1})
			continue
		}

		if style == stylePlain {
			if h, ok := plainHeading(line, i); ok {
				headings = append(headings, h)
			}
		}
	}

	return headings
}

func isUnderline(s string) bool {
	return setextRE.MatchString(s)
}

// plainHeading recognizes the heading conventions of a document that has no
// heading syntax: a numbered section, or a line opening with a section word.
func plainHeading(line string, index int) (heading, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || len([]rune(trimmed)) > maxHeadingRunes {
		return heading{}, false
	}

	if keywordRE.MatchString(trimmed) {
		return heading{level: 1, title: trimmed, line: index}, true
	}

	for _, re := range []*regexp.Regexp{multiLevelRE, singleLevelRE} {
		m := re.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}

		title := strings.TrimSpace(m[2])
		if title == "" {
			continue
		}

		// A heading is a label, so a line ending in sentence punctuation is
		// a numbered paragraph or a list item, not a section.
		if last, _ := utf8.DecodeLastRuneInString(title); strings.ContainsRune(".,;:", last) {
			continue
		}

		return heading{level: strings.Count(m[1], ".") + 1, title: trimmed, line: index}, true
	}

	return heading{}, false
}

// splitLevel picks the heading level to divide the document at: the
// shallowest one that yields more than one section. A document whose only
// level-1 heading is its own title should be split by its level-2 sections,
// not left whole.
func splitLevel(headings []heading) int {
	counts := map[int]int{}
	for _, h := range headings {
		counts[h.level]++
	}

	shallowest := headings[0].level
	for _, h := range headings {
		if h.level < shallowest {
			shallowest = h.level
		}
	}

	for level := shallowest; level <= 6; level++ {
		if counts[level] > 1 {
			return level
		}
	}

	return shallowest
}

// splitLeadingHeading peels a heading off the front of the preamble, so the
// document's own title becomes the unit's title instead of being repeated
// inside its body.
func splitLeadingHeading(preamble string) (title, body string) {
	first, rest, _ := strings.Cut(preamble, "\n")

	if m := atxRE.FindStringSubmatch(first); m != nil {
		return strings.TrimSpace(m[2]), strings.TrimSpace(rest)
	}

	// Setext: the title is the first line and the second is its underline.
	if second, remainder, ok := strings.Cut(rest, "\n"); ok && isUnderline(second) {
		return strings.TrimSpace(first), strings.TrimSpace(remainder)
	}
	if isUnderline(rest) {
		return strings.TrimSpace(first), ""
	}

	return titleFromBody(preamble), strings.TrimSpace(preamble)
}
