package convert

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
	"golang.org/x/net/html"
)

// blankLineRE splits text into paragraphs on one or more blank lines.
var blankLineRE = regexp.MustCompile(`\r?\n[ \t]*\r?\n+`)

// maxParagraphBytes caps how large a single blank-line-delimited block is
// allowed to become before splitParagraphs slices it further. Some PDF
// extractions (dense, two-column academic layouts especially) emit almost
// no blank lines at all, which would otherwise collapse the entire
// document into one enormous "paragraph" - this fallback keeps chunk sizes
// (and therefore chunk counts, for very large documents) reasonable even
// when the source gives splitParagraphs nothing useful to split on.
const maxParagraphBytes = 20_000

type sourceFormat int

const (
	formatUnknown sourceFormat = iota
	formatText
	formatMarkdown
	formatHTML
	formatCSV
	formatPDF
)

// detectFormat identifies the source format from the declared content type,
// falling back to the filename extension since browsers/clients don't
// always send a precise Content-Type for text-ish uploads.
func detectFormat(contentType, filename string) sourceFormat {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch ct {
	case "text/markdown", "text/x-markdown":
		return formatMarkdown
	case "text/html", "application/xhtml+xml":
		return formatHTML
	case "text/plain":
		return formatText
	case "text/csv", "application/csv", "application/vnd.ms-excel":
		return formatCSV
	case "application/pdf":
		return formatPDF
	}

	name := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(name, ".md"), strings.HasSuffix(name, ".markdown"):
		return formatMarkdown
	case strings.HasSuffix(name, ".html"), strings.HasSuffix(name, ".htm"):
		return formatHTML
	case strings.HasSuffix(name, ".txt"):
		return formatText
	case strings.HasSuffix(name, ".csv"):
		return formatCSV
	case strings.HasSuffix(name, ".pdf"):
		return formatPDF
	}

	return formatUnknown
}

// Supports reports whether the platform can convert a document of this
// format. The upload endpoint uses it to reject an unconvertible document at
// reception rather than accepting it and failing in a worker minutes later
// (see the recommendation in §11 of the brief: treat input documents as
// untrusted and validate format and size on the way in).
func Supports(contentType, filename string) bool {
	return detectFormat(contentType, filename) != formatUnknown
}

// SupportedFormatList names the accepted formats, for error messages shown
// to the user.
func SupportedFormatList() string {
	return "Markdown, HTML, texto plano, CSV o PDF"
}

func readText(r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("read text: %w", err)
	}
	return string(data), nil
}

func splitParagraphs(s string) []string {
	parts := blankLineRE.Split(s, -1)

	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, splitOversizedBlock(p)...)
	}
	return out
}

// splitOversizedBlock breaks s into maxParagraphBytes-sized pieces (without
// cutting a multi-byte UTF-8 rune in half) if it's larger than that,
// otherwise returns it unchanged as a single-element slice.
func splitOversizedBlock(s string) []string {
	if len(s) <= maxParagraphBytes {
		return []string{s}
	}

	var out []string
	for len(s) > maxParagraphBytes {
		cut := maxParagraphBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			// No rune boundary found in range (pathological input) - cut
			// at the byte limit anyway rather than looping forever.
			cut = maxParagraphBytes
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if len(s) > 0 {
		out = append(out, s)
	}
	return out
}

// extractCSV treats each row as its own record, joining fields with ", "
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

// blockTags are the HTML elements that end a line of text: whatever follows
// them starts a new block, which is what lets the Markdown segmenter see
// paragraph boundaries in a page that has no blank lines at all.
var blockTags = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "main": true,
	"header": true, "footer": true, "aside": true, "nav": true,
	"ul": true, "ol": true, "li": true, "dl": true, "dt": true, "dd": true,
	"table": true, "tr": true, "blockquote": true, "pre": true, "hr": true,
	"br": true, "figure": true, "figcaption": true,
}

// droppedTags hold content that is markup machinery rather than document
// text; their entire subtree is skipped.
var droppedTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"svg": true, "head": true,
}

// htmlToText renders an HTML document as Markdown-ish plain text: h1-h6
// become ATX headings and block elements become blank-line-separated
// paragraphs, so the same heading-based segmentation used for Markdown
// applies unchanged. It is deliberately not a full HTML-to-Markdown
// converter - the pipeline needs the document's *structure* (where the
// sections start) and its text, not faithful inline formatting.
func htmlToText(r io.Reader) (string, error) {
	z := html.NewTokenizer(r)

	var b strings.Builder
	var skipping string // tag whose subtree is being dropped, "" when none
	headingLevel := 0

	for {
		switch z.Next() {
		case html.ErrorToken:
			if err := z.Err(); err != io.EOF {
				return "", fmt.Errorf("read html: %w", err)
			}
			return normalizeBlankLines(b.String()), nil

		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			tag := string(name)

			if skipping != "" {
				continue
			}
			if droppedTags[tag] {
				skipping = tag
				continue
			}

			if level := headingTagLevel(tag); level > 0 {
				b.WriteString("\n\n" + strings.Repeat("#", level) + " ")
				headingLevel = level
				continue
			}
			if blockTags[tag] {
				b.WriteString("\n\n")
			}

		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)

			if skipping != "" {
				if skipping == tag {
					skipping = ""
				}
				continue
			}

			if headingTagLevel(tag) > 0 {
				headingLevel = 0
				b.WriteString("\n\n")
				continue
			}
			if blockTags[tag] {
				b.WriteString("\n\n")
			}

		case html.TextToken:
			if skipping != "" {
				continue
			}

			// Collapsing runs of whitespace mirrors how a browser renders
			// HTML, and keeps a heading on one line no matter how the source
			// was indented.
			text := strings.Join(strings.Fields(string(z.Text())), " ")
			if text == "" {
				continue
			}
			if headingLevel == 0 && needsSpace(b.String()) {
				b.WriteString(" ")
			}
			b.WriteString(text)
		}
	}
}

func headingTagLevel(tag string) int {
	if len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6' {
		return int(tag[1] - '0')
	}
	return 0
}

// needsSpace reports whether inline text should be separated from what was
// written before it, so two adjacent inline elements don't run together.
func needsSpace(written string) bool {
	if written == "" {
		return false
	}
	last := written[len(written)-1]
	return last != ' ' && last != '\n'
}

var manyBlankLinesRE = regexp.MustCompile(`\n{3,}`)

func normalizeBlankLines(s string) string {
	return strings.TrimSpace(manyBlankLinesRE.ReplaceAllString(strings.ReplaceAll(s, "\r\n", "\n"), "\n\n"))
}

// extractPDFText reads the whole file into memory (pdf.NewReader needs an
// io.ReaderAt) and pulls the document's text page by page. This scales with
// files.MaxFileSize: the worker holds the source bytes, the extracted text,
// and (in pipeline.go) the packaged bundle in memory at once, so a large
// upload means real peak RAM in this container.
func extractPDFText(r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("read pdf: %w", err)
	}

	doc, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open pdf: %w", err)
	}

	var buf strings.Builder
	for i := 1; i <= doc.NumPage(); i++ {
		writePageText(&buf, doc.Page(i).Content().Text)
	}

	return buf.String(), nil
}

// writePageText reconstructs readable text from chars, the page's
// per-character positions (as produced by Page.Content()).
//
// The pdf package's own Page.GetPlainText/Reader.GetPlainText just
// concatenate each Tj/TJ operator's decoded string with no regard for
// layout: PDFs commonly render justified or kerned text as a sequence of
// glyph runs positioned purely by numeric offsets, with no literal space
// character between words, which GetPlainText has no way to recover -
// the result is words glued together ("wordslikethis"). Since Content()
// gives each character's X/Y position, font size, and width, we can
// instead infer a word-space or line break from the geometric gap to the
// next character, the same technique real PDF text extractors (poppler,
// pdfminer, ...) use.
//
// Deliberately not attempted: guessing paragraph breaks (a blank line)
// from vertical gaps between lines. It sounds appealing, but footnote
// markers and other small-caps/superscript runs sit at a different
// baseline than the body text around them, which made a Y-gap heuristic
// fire constantly on ordinary inline text - it doesn't reliably
// distinguish "new paragraph" from "this line has a footnote marker in
// it," and on a real academic PDF that turned ~2,600 reasonable chunks
// into ~7,000 tiny ones (many just a stray 1-byte marker). Only a
// genuine page boundary is treated as a paragraph break; everything
// within a page beyond that relies on the segmenter's fallbacks.
func writePageText(buf *strings.Builder, chars []pdf.Text) {
	var prev pdf.Text
	havePrev := false

	for _, c := range chars {
		if c.S == "" {
			continue
		}

		if havePrev {
			threshold := math.Max(prev.FontSize, c.FontSize)
			if math.Abs(c.Y-prev.Y) < threshold*0.5 {
				// Same baseline. A horizontal gap wider than a small
				// kerning adjustment is a real word boundary.
				if c.X-(prev.X+prev.W) > prev.FontSize*0.2 {
					buf.WriteByte(' ')
				}
			} else {
				buf.WriteByte('\n')
			}
		}

		buf.WriteString(c.S)
		prev = c
		havePrev = true
	}

	buf.WriteString("\n\n")
}
