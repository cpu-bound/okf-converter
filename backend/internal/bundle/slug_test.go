package bundle

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"Introducción", "introduccion"},
		{"Metodología y diseño", "metodologia-y-diseno"},
		{"  Espacios   de sobra  ", "espacios-de-sobra"},
		{"¿Qué es OKF?", "que-es-okf"},
		{"Capítulo 1: el comienzo", "capitulo-1-el-comienzo"},
		{"C++ y C#", "c-y-c"},
		{"", ""},
		{"···", ""},
	}

	for _, tt := range tests {
		if got := slugify(tt.title); got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestSlugifyTruncatesWithoutTrailingDash(t *testing.T) {
	long := strings.Repeat("palabra ", 20)

	got := slugify(long)
	if len(got) > maxSlugLen {
		t.Errorf("slugify() = %q (%d bytes), want at most %d", got, len(got), maxSlugLen)
	}
	if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
		t.Errorf("slugify() = %q, want no leading or trailing dash", got)
	}
}

func TestUniqueSlug(t *testing.T) {
	taken := map[string]bool{}

	if got := uniqueSlug("ejemplo", "seccion-1", taken); got != "ejemplo" {
		t.Errorf("first uniqueSlug() = %q, want %q", got, "ejemplo")
	}
	if got := uniqueSlug("ejemplo", "seccion-2", taken); got != "ejemplo-2" {
		t.Errorf("second uniqueSlug() = %q, want %q", got, "ejemplo-2")
	}
	if got := uniqueSlug("ejemplo", "seccion-3", taken); got != "ejemplo-3" {
		t.Errorf("third uniqueSlug() = %q, want %q", got, "ejemplo-3")
	}
}

// An empty slug means the title had nothing usable in it (only punctuation
// or emoji); the caller's positional fallback has to take over so the unit
// still gets a file.
func TestUniqueSlugFallsBackWhenEmpty(t *testing.T) {
	taken := map[string]bool{}

	if got := uniqueSlug("", "seccion-4", taken); got != "seccion-4" {
		t.Errorf("uniqueSlug() = %q, want %q", got, "seccion-4")
	}
}
