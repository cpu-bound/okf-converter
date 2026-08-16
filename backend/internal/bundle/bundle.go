// Package bundle assembles an OKF knowledge bundle: a self-contained folder
// holding an index, a conversion log, and one Markdown document per logical
// unit found in the source.
//
//	bundle/
//	├── index.md      navegación y datos del bundle
//	├── log.md        trazabilidad de la conversión
//	└── 01-....md     un concepto por unidad lógica
//
// The layout follows Open Knowledge Format v0.1, which defines a bundle as a
// directory of Markdown files with YAML frontmatter: index.md and log.md are
// reserved filenames, concepts link to each other with ordinary Markdown
// links, and every file carries a `type` - the one field OKF requires (see
// frontmatter.go).
//
// A successful conversion is never a loose Markdown file - it is the whole
// bundle, which is why building it is one operation that either produces
// every required file or fails. The package knows nothing about storage,
// queues or HTTP: it turns units into files, and the caller decides where
// they go.
package bundle

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// The two files every bundle must contain, whatever the source document
// looked like.
const (
	IndexFile = "index.md"
	LogFile   = "log.md"
)

// Unit is one logical unit of the source document, in the order it appeared.
type Unit struct {
	// Title is the unit's heading. It may be empty, in which case Build
	// derives a placeholder - a document with no structure still has to
	// produce a usable bundle.
	Title string

	// Body is the unit's Markdown content, without the title heading (Build
	// writes that).
	Body string

	// Origin describes where the unit came from in the source document
	// ("encabezado nivel 2, línea 41"), for log.md. Optional.
	Origin string
}

// Source describes the document a bundle was built from, for index.md and
// log.md.
type Source struct {
	OriginalName string
	ContentType  string
	Size         int64
	FileID       string
	JobID        string
	ConvertedAt  time.Time
}

// File is one file inside the bundle, named relative to the bundle root.
type File struct {
	Name    string
	Content []byte
}

// Bundle is a complete OKF bundle, ready to be stored or packaged. Root is
// the folder name the files live under once packaged, so unzipping a bundle
// produces one directory rather than scattering files into the current one.
type Bundle struct {
	Root  string
	Files []File

	// Retained so RefreshLog can re-render log.md after the caller adds
	// entries that only exist once the bundle is built - validation results,
	// above all.
	src        Source
	title      string
	concepts   []concept
	log        *Log
	validation *Report
}

type concept struct {
	Unit
	Position int // 1-based, matching the order in the source document
	FileName string
}

// Build assembles the bundle. It records its own steps into log, which the
// caller is expected to have been writing to already, so log.md ends up
// holding the whole conversion and not just this last stage.
//
// A document with a single unit is an ordinary, complete bundle - not a
// degenerate case to warn about - so the only thing Build rejects is having
// no units at all, which means extraction produced nothing to write.
func Build(src Source, units []Unit, log *Log) (Bundle, error) {
	if len(units) == 0 {
		return Bundle{}, fmt.Errorf("no units to build a bundle from")
	}
	if log == nil {
		log = NewLog()
	}

	title := documentTitle(src.OriginalName)
	root := RootName(src.OriginalName)

	concepts := nameConcepts(units)
	log.Step("%d unidad(es) de conocimiento nombradas y ordenadas", len(concepts))

	b := Bundle{
		Root:     root,
		src:      src,
		title:    title,
		concepts: concepts,
		log:      log,
	}

	rc := renderContext{title: title, root: root, src: src, concepts: concepts}

	for _, c := range concepts {
		b.Files = append(b.Files, File{Name: c.FileName, Content: []byte(renderConcept(rc, c))})
	}
	log.Step("%d documento(s) de concepto generados en Markdown con frontmatter OKF", len(concepts))

	b.Files = append(b.Files, File{Name: IndexFile, Content: []byte(renderIndex(rc))})
	log.Step("index.md generado con %d enlace(s) en el orden del documento de origen", len(concepts))

	b.Files = append(b.Files, File{Name: LogFile, Content: nil})
	b.RefreshLog()

	return b, nil
}

// RefreshLog re-renders log.md from the Log the bundle was built with. It
// exists because some of what belongs in the log - the validation verdict
// above all - is only known once the bundle exists, and log.md has to end up
// carrying it.
func (b *Bundle) RefreshLog() {
	rc := renderContext{title: b.title, root: b.Root, src: b.src, concepts: b.concepts}
	content := []byte(renderLog(rc, b.log, b.validation))
	for i := range b.Files {
		if b.Files[i].Name == LogFile {
			b.Files[i].Content = content
			return
		}
	}
	b.Files = append(b.Files, File{Name: LogFile, Content: content})
}

// Log returns the log the bundle was built with, so callers can append
// entries and then call RefreshLog.
func (b *Bundle) Log() *Log { return b.log }

// SetValidation attaches the verdict of Validate, so the next RefreshLog
// writes the rule-by-rule outcome into log.md - the §3 requirement that the
// log record the validations the bundle went through, including the ones it
// passed.
//
// Note that log.md is itself one of the validated files, so the report
// describes the bundle as it stood *before* this call. That is deliberate:
// the alternative is a log that has to describe its own contents.
func (b *Bundle) SetValidation(r Report) {
	b.validation = &r
	b.RefreshLog()
}

// Find returns the content of the named file within the bundle.
func (b Bundle) Find(name string) ([]byte, bool) {
	for _, f := range b.Files {
		if f.Name == name {
			return f.Content, true
		}
	}
	return nil, false
}

// ConceptCount reports how many concept documents the bundle holds - that
// is, every file except index.md and log.md.
func (b Bundle) ConceptCount() int { return len(b.concepts) }

// nameConcepts assigns each unit its position and file name, resolving
// duplicate titles so two sections sharing a heading still get two files.
func nameConcepts(units []Unit) []concept {
	taken := map[string]bool{}
	concepts := make([]concept, 0, len(units))

	for i, u := range units {
		position := i + 1

		title := strings.TrimSpace(u.Title)
		if title == "" {
			title = fmt.Sprintf("Sección %d", position)
		}

		slug := uniqueSlug(slugify(title), fmt.Sprintf("seccion-%d", position), taken)

		u.Title = title
		concepts = append(concepts, concept{
			Unit:     u,
			Position: position,
			// The numeric prefix keeps the source document's order visible in
			// a plain file listing, where names would otherwise sort
			// alphabetically and lose it.
			FileName: fmt.Sprintf("%02d-%s.md", position, slug),
		})
	}

	return concepts
}

// renderContext is what every file of the bundle needs to know about the
// bundle it belongs to in order to render itself.
type renderContext struct {
	title    string
	root     string
	src      Source
	concepts []concept
}

// tags are the OKF tags every file in this bundle carries: one marking the
// format, one identifying the bundle itself.
func (rc renderContext) tags() []string {
	return []string{"okf", rc.root}
}

func renderIndex(rc renderContext) string {
	title, src, concepts := rc.title, rc.src, rc.concepts

	var b strings.Builder

	b.WriteString(frontmatter{
		Type:        TypeIndex,
		Title:       title,
		Description: fmt.Sprintf("Índice del bundle OKF generado a partir de «%s», con %d unidad(es) de conocimiento.", src.OriginalName, len(concepts)),
		Tags:        rc.tags(),
		Timestamp:   src.ConvertedAt,
		Source:      src.OriginalName,
	}.render())

	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "Bundle de conocimiento en formato OKF, generado a partir de `%s`.\n\n", src.OriginalName)

	b.WriteString("## Datos del bundle\n\n")
	b.WriteString("| Campo | Valor |\n| --- | --- |\n")
	fmt.Fprintf(&b, "| Documento de origen | `%s` |\n", src.OriginalName)
	fmt.Fprintf(&b, "| Tipo de contenido | %s |\n", orDash(src.ContentType))
	fmt.Fprintf(&b, "| Tamaño del original | %s |\n", humanSize(src.Size))
	fmt.Fprintf(&b, "| Unidades de conocimiento | %d |\n", len(concepts))
	fmt.Fprintf(&b, "| Convertido | %s |\n", formatTime(src.ConvertedAt))
	fmt.Fprintf(&b, "| Trabajo de conversión | `%s` |\n\n", orDash(src.JobID))

	b.WriteString("## Conceptos\n\n")
	for _, c := range concepts {
		fmt.Fprintf(&b, "%d. [%s](%s)\n", c.Position, escapeLinkText(c.Title), c.FileName)
	}

	fmt.Fprintf(&b, "\n## Trazabilidad\n\nLa bitácora completa de la conversión está en [log.md](%s).\n", LogFile)

	return b.String()
}

func renderConcept(rc renderContext, c concept) string {
	all := rc.concepts

	var b strings.Builder

	b.WriteString(frontmatter{
		Type:        TypeConcept,
		Title:       c.Title,
		Description: fmt.Sprintf("Unidad %d de %d del documento «%s».", c.Position, len(all), rc.src.OriginalName),
		Tags:        rc.tags(),
		Timestamp:   rc.src.ConvertedAt,
		Source:      rc.src.OriginalName,
	}.render())

	fmt.Fprintf(&b, "# %s\n\n", c.Title)

	body := strings.TrimSpace(c.Body)
	if body == "" {
		// A heading with nothing under it is still a unit of the source
		// document, and dropping it would break the index's ordering.
		body = "_Esta unidad no tiene contenido en el documento de origen._"
	}
	b.WriteString(body)
	b.WriteString("\n\n---\n\n")

	// Navigation footer: every concept is reachable from its neighbours and
	// from the index, so the bundle can be read as a whole rather than as a
	// pile of files.
	fmt.Fprintf(&b, "[Índice](%s)", IndexFile)
	if i := c.Position - 1; i > 0 {
		prev := all[i-1]
		fmt.Fprintf(&b, " · Anterior: [%s](%s)", escapeLinkText(prev.Title), prev.FileName)
	}
	if c.Position < len(all) {
		next := all[c.Position]
		fmt.Fprintf(&b, " · Siguiente: [%s](%s)", escapeLinkText(next.Title), next.FileName)
	}
	b.WriteString("\n")

	return b.String()
}

func renderLog(rc renderContext, log *Log, validation *Report) string {
	title, src, concepts := rc.title, rc.src, rc.concepts

	var b strings.Builder

	b.WriteString(frontmatter{
		Type:        TypeLog,
		Title:       "Bitácora de conversión — " + title,
		Description: fmt.Sprintf("Trazabilidad de la conversión de «%s» a un bundle OKF.", src.OriginalName),
		Tags:        rc.tags(),
		Timestamp:   src.ConvertedAt,
		Source:      src.OriginalName,
	}.render())

	fmt.Fprintf(&b, "# Bitácora de conversión — %s\n\n", title)
	fmt.Fprintf(&b, "- **Documento de origen:** `%s` (%s, %s)\n", src.OriginalName, orDash(src.ContentType), humanSize(src.Size))
	fmt.Fprintf(&b, "- **Archivo:** `%s`\n", orDash(src.FileID))
	fmt.Fprintf(&b, "- **Trabajo de conversión:** `%s`\n", orDash(src.JobID))
	fmt.Fprintf(&b, "- **Convertido:** %s\n\n", formatTime(src.ConvertedAt))

	b.WriteString("## Operaciones\n\n")
	if len(log.Entries()) == 0 {
		b.WriteString("_Sin operaciones registradas._\n")
	}
	for _, e := range log.Entries() {
		fmt.Fprintf(&b, "- `%s` %s\n", e.At.Format("15:04:05.000"), e.Message)
	}

	b.WriteString("\n## Unidades detectadas\n\n")
	b.WriteString("| # | Título | Documento | Origen | Caracteres |\n| --- | --- | --- | --- | --- |\n")
	for _, c := range concepts {
		fmt.Fprintf(&b, "| %d | %s | `%s` | %s | %d |\n",
			c.Position,
			escapeTableCell(c.Title),
			c.FileName,
			escapeTableCell(orDash(c.Origin)),
			len([]rune(c.Body)),
		)
	}

	if validation != nil {
		b.WriteString("\n")
		b.WriteString(renderValidation(*validation))
	}

	return b.String()
}

// renderValidation writes the rule-by-rule outcome into log.md. Every rule is
// listed, passed or not: the §3 requirement is to record "las validaciones
// superadas", so a log that only showed failures would leave the reader
// unable to tell what was actually checked.
func renderValidation(r Report) string {
	var b strings.Builder

	b.WriteString("## Validación\n\n")
	fmt.Fprintf(&b, "- **Resultado:** %s\n", r.Verdict.Label())
	fmt.Fprintf(&b, "- **Validez de plataforma:** %s\n", r.Platform.Label())
	fmt.Fprintf(&b, "- **Conformidad OKF:** %s\n\n", r.OKF.Label())

	b.WriteString("| Regla | Ámbito | Severidad | Resultado |\n| --- | --- | --- | --- |\n")
	for _, c := range r.Checks {
		outcome := "superada"
		if !c.Passed {
			outcome = "**no superada**"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n",
			escapeTableCell(c.Rule), scopeLabel(c.Scope), severityLabel(c.Severity), outcome)
	}

	failures := r.Failures()
	if len(failures) == 0 {
		return b.String()
	}

	b.WriteString("\n### Hallazgos\n\n")
	for _, c := range failures {
		fmt.Fprintf(&b, "- **%s** (%s)\n", escapeLinkText(c.Rule), severityLabel(c.Severity))
		for _, detail := range c.Details {
			fmt.Fprintf(&b, "  - %s\n", detail)
		}
	}

	return b.String()
}

func scopeLabel(s Scope) string {
	if s == ScopeOKF {
		return "OKF"
	}
	return "plataforma"
}

func severityLabel(s Severity) string {
	if s == SeverityWarning {
		return "advertencia"
	}
	return "error"
}

// RootName is the folder name the bundle for originalName is packaged under.
// Exported so the handler that serves the download can name the .zip after
// the folder it contains without the bundle having to be rebuilt or the name
// stored anywhere - the same reasoning behind the deterministic object keys.
func RootName(originalName string) string {
	root := slugify(documentTitle(originalName))
	if root == "" {
		return "bundle"
	}
	return root
}

// documentTitle derives a human title from the uploaded file name: drop the
// extension, and turn the separators people actually use in file names into
// spaces. Accents are kept - this is display text, unlike a slug.
func documentTitle(originalName string) string {
	name := path.Base(strings.TrimSpace(originalName))
	if name == "" || name == "." || name == "/" {
		return "Documento"
	}

	if ext := path.Ext(name); ext != "" && ext != name {
		name = strings.TrimSuffix(name, ext)
	}

	name = strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(name)
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "Documento"
	}

	return strings.ToUpper(name[:1]) + name[1:]
}

// escapeLinkText keeps a title containing brackets from breaking the
// Markdown link it sits in.
func escapeLinkText(s string) string {
	return strings.NewReplacer("[", `\[`, "]", `\]`).Replace(s)
}

// escapeTableCell keeps a title containing a pipe or a newline from breaking
// the Markdown table row it sits in.
func escapeTableCell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ", "\r", "").Replace(s)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func humanSize(bytes int64) string {
	switch {
	case bytes < 0:
		return "—"
	case bytes < 1024:
		return fmt.Sprintf("%d B", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
}
