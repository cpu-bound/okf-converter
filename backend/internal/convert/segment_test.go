package convert

import (
	"strings"
	"testing"

	"okf-converter/backend/internal/bundle"
)

func segment(t *testing.T, text string, style headingStyle) []bundle.Unit {
	t.Helper()
	return segmentText(text, style, bundle.NewLog())
}

func titles(units []bundle.Unit) []string {
	out := make([]string, 0, len(units))
	for _, u := range units {
		out = append(out, u.Title)
	}
	return out
}

// The brief's "documento breve" case: a short document with no divisions is
// one concept, and the pipeline must not treat that as unusual.
func TestSegmentShortDocumentWithoutHeadings(t *testing.T) {
	units := segment(t, "Una nota corta.\n\nCon dos párrafos, pero sin encabezados.", stylePlain)

	if len(units) != 1 {
		t.Fatalf("got %d units, want 1:\n%v", len(units), titles(units))
	}
	if !strings.Contains(units[0].Body, "dos párrafos") {
		t.Errorf("the single unit lost content: %q", units[0].Body)
	}
}

// The brief's "documento estructurado" case: one concept per section, in the
// source document's order.
func TestSegmentMarkdownByHeadings(t *testing.T) {
	const doc = `# Introducción

Texto de la introducción.

# Metodología

Cómo se hizo.

# Conclusiones

Qué salió.`

	units := segment(t, doc, styleMarkup)

	want := []string{"Introducción", "Metodología", "Conclusiones"}
	if got := titles(units); !equalStrings(got, want) {
		t.Fatalf("titles = %v, want %v", got, want)
	}
	if !strings.Contains(units[1].Body, "Cómo se hizo.") {
		t.Errorf("unit 2 body = %q, want the section's own text", units[1].Body)
	}
	if strings.Contains(units[1].Body, "Introducción") {
		t.Errorf("unit 2 body leaked the previous section: %q", units[1].Body)
	}
}

// A document whose only level-1 heading is its own title has to be divided
// by its level-2 sections, not left whole.
func TestSegmentDescendsPastASingleTopHeading(t *testing.T) {
	const doc = `# Informe anual

Resumen ejecutivo.

## Ventas

Subieron.

## Costos

Bajaron.`

	units := segment(t, doc, styleMarkup)

	want := []string{"Informe anual", "Ventas", "Costos"}
	if got := titles(units); !equalStrings(got, want) {
		t.Fatalf("titles = %v, want %v (preamble plus one unit per section)", got, want)
	}
	if units[0].Body != "Resumen ejecutivo." {
		t.Errorf("preamble body = %q, want the text under the title without the title itself", units[0].Body)
	}
}

// Subsections belong to their parent section rather than becoming units of
// their own.
func TestSegmentKeepsSubsectionsInsideTheirSection(t *testing.T) {
	const doc = `## Ventas

### Primer trimestre

Bien.

### Segundo trimestre

Mejor.

## Costos

Estables.`

	units := segment(t, doc, styleMarkup)

	if got := titles(units); !equalStrings(got, []string{"Ventas", "Costos"}) {
		t.Fatalf("titles = %v, want [Ventas Costos]", got)
	}
	if !strings.Contains(units[0].Body, "### Primer trimestre") {
		t.Errorf("subsections were dropped from their parent: %q", units[0].Body)
	}
}

// A document title with no content under it is not a concept - it would
// produce an empty file that only repeats the title.
func TestSegmentSkipsAnEmptyPreamble(t *testing.T) {
	const doc = `# Informe

## Ventas

Subieron.

## Costos

Bajaron.`

	if got := titles(segment(t, doc, styleMarkup)); !equalStrings(got, []string{"Ventas", "Costos"}) {
		t.Errorf("titles = %v, want [Ventas Costos] with no empty preamble unit", got)
	}
}

func TestSegmentSetextHeadings(t *testing.T) {
	const doc = `Introducción
============

Primera parte.

Conclusiones
============

Última parte.`

	if got := titles(segment(t, doc, styleMarkup)); !equalStrings(got, []string{"Introducción", "Conclusiones"}) {
		t.Errorf("titles = %v, want [Introducción Conclusiones]", got)
	}
}

// A '#' inside a fenced code block is code, not a heading.
func TestSegmentIgnoresHeadingsInsideCodeFences(t *testing.T) {
	const doc = "# Uno\n\n```bash\n# esto es un comentario\necho hola\n```\n\n# Dos\n\nfinal."

	if got := titles(segment(t, doc, styleMarkup)); !equalStrings(got, []string{"Uno", "Dos"}) {
		t.Errorf("titles = %v, want [Uno Dos]", got)
	}
}

func TestSegmentPlainTextNumberedSections(t *testing.T) {
	const doc = `1. Introducción

Texto de apertura.

2. Desarrollo

Cuerpo del documento.

3. Cierre

Texto final.`

	got := titles(segment(t, doc, stylePlain))
	want := []string{"1. Introducción", "2. Desarrollo", "3. Cierre"}
	if !equalStrings(got, want) {
		t.Errorf("titles = %v, want %v", got, want)
	}
}

func TestSegmentPlainTextKeywordSections(t *testing.T) {
	const doc = `Capítulo I: El comienzo

Érase una vez.

Capítulo II: El nudo

Se complicó.`

	got := titles(segment(t, doc, stylePlain))
	if len(got) != 2 || !strings.HasPrefix(got[0], "Capítulo I") || !strings.HasPrefix(got[1], "Capítulo II") {
		t.Errorf("titles = %v, want the two chapters", got)
	}
}

// Numbered headings are only recognized in plain text, where there is no
// heading syntax to rely on - and a year at the start of a sentence must not
// be mistaken for one.
func TestSegmentDoesNotMistakeSentencesForNumberedHeadings(t *testing.T) {
	const doc = `2024 fue un año difícil

El contexto económico se deterioró.

2025 trajo una recuperación

Los indicadores mejoraron.`

	if got := segment(t, doc, stylePlain); len(got) != 1 {
		t.Errorf("got %d units, want 1 - no line here is a heading:\n%v", len(got), titles(got))
	}
}

// A large document with no structure at all still has to produce usable
// units rather than one enormous concept.
func TestSegmentLargeUnstructuredDocumentFallsBackToParagraphs(t *testing.T) {
	paragraph := strings.Repeat("palabra ", 500)
	doc := strings.TrimSpace(strings.Repeat(paragraph+"\n\n", 10))

	units := segment(t, doc, stylePlain)
	if len(units) < 2 {
		t.Fatalf("got %d units, want the paragraph fallback to kick in", len(units))
	}
	for i, u := range units {
		if !strings.Contains(u.Origin, "sin estructura detectada") {
			t.Errorf("unit %d origin = %q, want it to say no structure was found", i, u.Origin)
		}
	}
}

func TestSegmentEmptyDocument(t *testing.T) {
	if got := segment(t, "   \n\n  \n", stylePlain); len(got) != 0 {
		t.Errorf("got %d units for an empty document, want 0", len(got))
	}
}

func TestSegmentRecordsWhereEachUnitCameFrom(t *testing.T) {
	units := segment(t, "# Uno\n\na\n\n# Dos\n\nb", styleMarkup)

	if len(units) != 2 {
		t.Fatalf("got %d units, want 2", len(units))
	}
	if !strings.Contains(units[1].Origin, "línea 5") {
		t.Errorf("unit 2 origin = %q, want it to point at the heading's line", units[1].Origin)
	}
}
