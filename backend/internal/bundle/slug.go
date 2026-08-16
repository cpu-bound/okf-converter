package bundle

import (
	"fmt"
	"strings"
	"unicode"
)

// maxSlugLen keeps generated file names short enough to stay readable in a
// file listing. Titles are frequently a full sentence, and the slug only has
// to be recognizable - the real title lives inside the file and in index.md.
const maxSlugLen = 48

// foldings maps the accented characters Spanish (and the odd loanword) puts
// in headings onto their ASCII equivalent. Doing it with a table rather than
// Unicode normalization keeps bundle file names dependency-free and, more
// importantly, predictable: "ñ" becomes "n" rather than disappearing.
var foldings = map[rune]string{
	'á': "a", 'à': "a", 'ä': "a", 'â': "a", 'ã': "a", 'å': "a",
	'é': "e", 'è': "e", 'ë': "e", 'ê': "e",
	'í': "i", 'ì': "i", 'ï': "i", 'î': "i",
	'ó': "o", 'ò': "o", 'ö': "o", 'ô': "o", 'õ': "o",
	'ú': "u", 'ù': "u", 'ü': "u", 'û': "u",
	'ñ': "n", 'ç': "c",
	'ß': "ss", 'æ': "ae", 'ø': "o",
}

// slugify turns a human title into a file-name-safe ASCII slug. It returns
// an empty string when nothing usable survives (a title made only of emoji
// or punctuation, say) - callers decide what to fall back to, since the
// right fallback differs between a concept file and the bundle root.
func slugify(title string) string {
	var b strings.Builder

	lastDash := true // leading dashes are suppressed
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if folded, ok := foldings[r]; ok {
			b.WriteString(folded)
			lastDash = false
			continue
		}

		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}

	return strings.Trim(truncateSlug(b.String()), "-")
}

// truncateSlug cuts the slug at maxSlugLen without leaving a half-word when
// it can avoid it: if there's a dash reasonably close to the limit, it cuts
// there instead.
func truncateSlug(s string) string {
	if len(s) <= maxSlugLen {
		return s
	}

	cut := s[:maxSlugLen]
	if idx := strings.LastIndexByte(cut, '-'); idx > maxSlugLen/2 {
		return cut[:idx]
	}
	return cut
}

// uniqueSlug returns slug (or fallback, when slug is empty) adjusted so it
// doesn't collide with anything already in taken, and records the result.
// Two sections legitimately share a title often enough - "Introducción" in
// two different parts of a document - that a collision has to produce a
// second usable file rather than one file overwriting the other.
func uniqueSlug(slug, fallback string, taken map[string]bool) string {
	if slug == "" {
		slug = fallback
	}

	candidate := slug
	for n := 2; taken[candidate]; n++ {
		candidate = fmt.Sprintf("%s-%d", slug, n)
	}

	taken[candidate] = true
	return candidate
}
