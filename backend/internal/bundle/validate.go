package bundle

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Validation answers two different questions about a freshly built bundle,
// and the enunciado (§5.2) asks for them separately:
//
//   - Platform validity: is this a usable bundle for *this* platform? The
//     minimum structure (index.md + log.md + at least one concept) and an
//     index whose links actually resolve.
//   - OKF conformance: does it match Open Knowledge Format v0.1? Frontmatter
//     on every file, a `type` on every file (the one field OKF requires), and
//     the standard queryable fields.
//
// A bundle that fails either one with an error-severity rule is never
// published: the conversion is treated as failed, nothing reaches object
// storage, and no download is offered (§6).
type Scope string

const (
	ScopePlatform Scope = "platform"
	ScopeOKF      Scope = "okf"
)

// Severity is what a failing rule costs. An error makes the bundle invalid; a
// warning leaves it publishable but flagged.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Verdict is the classification the enunciado asks for: valid, valid with
// warnings, or invalid.
type Verdict string

const (
	VerdictValid        Verdict = "valid"
	VerdictWithWarnings Verdict = "valid_with_warnings"
	VerdictInvalid      Verdict = "invalid"
)

// Publishable reports whether a bundle classified this way may be stored and
// offered for download.
func (v Verdict) Publishable() bool { return v != VerdictInvalid }

// Label is the verdict in the language the log and the UI speak.
func (v Verdict) Label() string {
	switch v {
	case VerdictValid:
		return "válido"
	case VerdictWithWarnings:
		return "válido con advertencias"
	case VerdictInvalid:
		return "inválido"
	default:
		return string(v)
	}
}

// Check is the outcome of one validation rule. Severity is what failing it
// costs, so a passing check still says how much it would have mattered.
type Check struct {
	ID       string   `json:"id"`
	Scope    Scope    `json:"scope"`
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	Passed   bool     `json:"passed"`
	// Details explains a failure in terms of the bundle ("el enlace
	// `03-x.md` de index.md no existe"). Empty when the check passed.
	Details []string `json:"details,omitempty"`
}

// Report is the full validation result, persisted alongside the file so the
// frontend can show not just the verdict but which rule produced it.
type Report struct {
	Verdict  Verdict `json:"verdict"`
	Platform Verdict `json:"platform"`
	OKF      Verdict `json:"okf"`
	Checks   []Check `json:"checks"`
}

// Failures returns the checks that did not pass, worst first.
func (r Report) Failures() []Check {
	var errs, warns []Check
	for _, c := range r.Checks {
		if c.Passed {
			continue
		}
		if c.Severity == SeverityError {
			errs = append(errs, c)
		} else {
			warns = append(warns, c)
		}
	}
	return append(errs, warns...)
}

// Summary is a one-line rendering of the verdict and what drove it, for the
// error recorded against a failed job.
func (r Report) Summary() string {
	failures := r.Failures()
	if len(failures) == 0 {
		return r.Verdict.Label()
	}

	reasons := make([]string, 0, len(failures))
	for _, c := range failures {
		reasons = append(reasons, c.Rule)
	}
	return r.Verdict.Label() + ": " + strings.Join(reasons, "; ")
}

// Validate runs every rule against the bundle and classifies the result. It
// reads only the bundle's rendered bytes - not the internal state Build used
// to produce them - so it checks the artifact that would actually be
// published rather than the intent behind it.
func Validate(b Bundle) Report {
	files := map[string][]byte{}
	for _, f := range b.Files {
		files[f.Name] = f.Content
	}

	checks := []Check{
		minimumStructure(files),
		indexLinksResolve(files),
		everyConceptIsIndexed(files),
		conceptLinksResolve(files),

		frontmatterPresent(files),
		typeFieldPresent(files),
		standardFieldsPresent(files),
		reservedTypesMatch(files),
	}

	r := Report{Checks: checks}
	r.Platform = verdictFor(checks, ScopePlatform)
	r.OKF = verdictFor(checks, ScopeOKF)
	r.Verdict = worst(r.Platform, r.OKF)
	return r
}

// verdictFor classifies one scope: any failed error makes it invalid, any
// failed warning downgrades it, otherwise it is clean.
func verdictFor(checks []Check, scope Scope) Verdict {
	verdict := VerdictValid
	for _, c := range checks {
		if c.Scope != scope || c.Passed {
			continue
		}
		if c.Severity == SeverityError {
			return VerdictInvalid
		}
		verdict = VerdictWithWarnings
	}
	return verdict
}

func worst(verdicts ...Verdict) Verdict {
	rank := map[Verdict]int{VerdictValid: 0, VerdictWithWarnings: 1, VerdictInvalid: 2}
	result := VerdictValid
	for _, v := range verdicts {
		if rank[v] > rank[result] {
			result = v
		}
	}
	return result
}

// --- Platform validity ------------------------------------------------------

// minimumStructure is the rule §6 names explicitly: without index.md or
// log.md the bundle is not published and no download is enabled.
func minimumStructure(files map[string][]byte) Check {
	c := Check{
		ID:       "minimum-structure",
		Scope:    ScopePlatform,
		Rule:     "El bundle contiene index.md, log.md y al menos un concepto",
		Severity: SeverityError,
	}

	for _, name := range []string{IndexFile, LogFile} {
		if content, ok := files[name]; !ok {
			c.Details = append(c.Details, fmt.Sprintf("falta `%s`", name))
		} else if len(strings.TrimSpace(string(content))) == 0 {
			c.Details = append(c.Details, fmt.Sprintf("`%s` está vacío", name))
		}
	}

	if conceptFiles(files) == 0 {
		c.Details = append(c.Details, "no hay ningún documento de concepto")
	}

	c.Passed = len(c.Details) == 0
	return c
}

// indexLinksResolve is the second rule the enunciado names: every link in
// index.md must point at a file the bundle actually holds. index.md is fully
// generated, so a broken link there is a defect of ours, not of the source.
func indexLinksResolve(files map[string][]byte) Check {
	c := Check{
		ID:       "index-links",
		Scope:    ScopePlatform,
		Rule:     "Todos los enlaces de index.md resuelven dentro del bundle",
		Severity: SeverityError,
	}

	index, ok := files[IndexFile]
	if !ok {
		c.Details = append(c.Details, "no hay index.md que revisar")
		return c
	}

	for _, target := range relativeLinks(index) {
		if _, ok := files[target]; !ok {
			c.Details = append(c.Details, fmt.Sprintf("`%s` enlaza a `%s`, que no está en el bundle", IndexFile, target))
		}
	}

	c.Passed = len(c.Details) == 0
	return c
}

// everyConceptIsIndexed catches the opposite defect: a concept that exists as
// a file but is unreachable from the index, which would make it invisible to
// anyone reading the bundle from its entry point.
func everyConceptIsIndexed(files map[string][]byte) Check {
	c := Check{
		ID:       "concepts-indexed",
		Scope:    ScopePlatform,
		Rule:     "Todos los conceptos son alcanzables desde index.md",
		Severity: SeverityError,
	}

	index, ok := files[IndexFile]
	if !ok {
		c.Details = append(c.Details, "no hay index.md que revisar")
		return c
	}

	linked := map[string]bool{}
	for _, target := range relativeLinks(index) {
		linked[target] = true
	}

	for name := range files {
		if isReserved(name) || linked[name] {
			continue
		}
		c.Details = append(c.Details, fmt.Sprintf("`%s` no está enlazado desde index.md", name))
	}

	c.Passed = len(c.Details) == 0
	return c
}

// conceptLinksResolve is a warning rather than an error because a concept's
// body carries the source document's own Markdown: a relative link the author
// wrote to a file outside the bundle is a real defect to report, but not one
// worth refusing to publish over.
func conceptLinksResolve(files map[string][]byte) Check {
	c := Check{
		ID:       "concept-links",
		Scope:    ScopePlatform,
		Rule:     "Los enlaces relativos dentro de los conceptos resuelven",
		Severity: SeverityWarning,
	}

	for _, name := range sortedNames(files) {
		if name == IndexFile {
			continue
		}
		for _, target := range relativeLinks(files[name]) {
			if _, ok := files[target]; !ok {
				c.Details = append(c.Details, fmt.Sprintf("`%s` enlaza a `%s`, que no está en el bundle", name, target))
			}
		}
	}

	c.Passed = len(c.Details) == 0
	return c
}

// --- OKF conformance --------------------------------------------------------

func frontmatterPresent(files map[string][]byte) Check {
	c := Check{
		ID:       "okf-frontmatter",
		Scope:    ScopeOKF,
		Rule:     "Todos los archivos abren con un bloque de frontmatter YAML",
		Severity: SeverityError,
	}

	for _, name := range sortedNames(files) {
		if _, ok := parseFrontmatter(files[name]); !ok {
			c.Details = append(c.Details, fmt.Sprintf("`%s` no tiene frontmatter", name))
		}
	}

	c.Passed = len(c.Details) == 0
	return c
}

// typeFieldPresent checks the single field OKF v0.1 requires of every file.
func typeFieldPresent(files map[string][]byte) Check {
	c := Check{
		ID:       "okf-type",
		Scope:    ScopeOKF,
		Rule:     "Todos los archivos declaran el campo obligatorio `type`",
		Severity: SeverityError,
	}

	for _, name := range sortedNames(files) {
		fields, ok := parseFrontmatter(files[name])
		if !ok {
			c.Details = append(c.Details, fmt.Sprintf("`%s` no tiene frontmatter donde declarar `type`", name))
			continue
		}
		if strings.TrimSpace(fields["type"]) == "" {
			c.Details = append(c.Details, fmt.Sprintf("`%s` no declara `type`", name))
		}
	}

	c.Passed = len(c.Details) == 0
	return c
}

// okfStandardFields are the fields OKF names as queryable. Only `type` is
// required, so their absence is a warning: it costs consumers something, but
// the bundle is still conformant.
var okfStandardFields = []string{"title", "description", "tags", "timestamp"}

func standardFieldsPresent(files map[string][]byte) Check {
	c := Check{
		ID:       "okf-standard-fields",
		Scope:    ScopeOKF,
		Rule:     "Todos los archivos traen los campos estándar consultables de OKF",
		Severity: SeverityWarning,
	}

	for _, name := range sortedNames(files) {
		fields, ok := parseFrontmatter(files[name])
		if !ok {
			continue // already reported by frontmatterPresent
		}

		var missing []string
		for _, field := range okfStandardFields {
			if strings.TrimSpace(fields[field]) == "" {
				missing = append(missing, field)
			}
		}
		if len(missing) > 0 {
			c.Details = append(c.Details, fmt.Sprintf("`%s` no trae %s", name, strings.Join(missing, ", ")))
		}
	}

	c.Passed = len(c.Details) == 0
	return c
}

// reservedTypesMatch checks that the two filenames OKF reserves carry the
// types that match their role, so a consumer can find the entry point and the
// provenance trail by querying `type` rather than by filename convention.
func reservedTypesMatch(files map[string][]byte) Check {
	c := Check{
		ID:       "okf-reserved-types",
		Scope:    ScopeOKF,
		Rule:     "index.md y log.md declaran los tipos que corresponden a su rol",
		Severity: SeverityWarning,
	}

	for name, want := range map[string]string{IndexFile: TypeIndex, LogFile: TypeLog} {
		content, ok := files[name]
		if !ok {
			continue // already reported by minimumStructure
		}
		fields, ok := parseFrontmatter(content)
		if !ok {
			continue
		}
		if got := unquoteYAML(fields["type"]); got != want {
			c.Details = append(c.Details, fmt.Sprintf("`%s` declara type %q, se esperaba %q", name, got, want))
		}
	}

	c.Passed = len(c.Details) == 0
	return c
}

// --- helpers ----------------------------------------------------------------

// markdownLinkRE matches an inline Markdown link and captures its target. The
// link text may contain escaped brackets - escapeLinkText puts them there
// whenever a heading does - so a naive `[^\]]*` would skip exactly the links
// most likely to be mis-generated.
var markdownLinkRE = regexp.MustCompile(`\[(?:\\.|[^\]\\])*\]\(([^)\s]+)`)

// relativeLinks returns the in-bundle targets a file links to: absolute URLs,
// anchors and absolute paths point outside the bundle and are not ours to
// resolve.
func relativeLinks(content []byte) []string {
	var targets []string

	for _, m := range markdownLinkRE.FindAllSubmatch(content, -1) {
		target := string(m[1])

		if strings.Contains(target, "://") || strings.HasPrefix(target, "#") ||
			strings.HasPrefix(target, "/") || strings.HasPrefix(target, "mailto:") {
			continue
		}

		// A link into a file's heading still names the file.
		if i := strings.IndexByte(target, '#'); i >= 0 {
			target = target[:i]
		}
		target = strings.TrimPrefix(target, "./")

		if target != "" {
			targets = append(targets, target)
		}
	}

	return targets
}

// frontmatterRE captures the YAML block a file opens with, if any.
var frontmatterRE = regexp.MustCompile(`(?s)\A---\r?\n(.*?)\r?\n---\r?\n`)

// fieldRE matches one top-level `key: value` line of that block. The
// generator writes flat frontmatter, so nested YAML is out of scope.
var fieldRE = regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_-]*):[ \t]*(.*)$`)

// parseFrontmatter reads the file's leading YAML block into a flat map. It is
// deliberately minimal: enough to tell whether the fields OKF names are there,
// not a YAML parser.
func parseFrontmatter(content []byte) (map[string]string, bool) {
	m := frontmatterRE.FindSubmatch(content)
	if m == nil {
		return nil, false
	}

	fields := map[string]string{}
	for _, f := range fieldRE.FindAllSubmatch(m[1], -1) {
		fields[string(f[1])] = strings.TrimSpace(string(f[2]))
	}

	return fields, true
}

// unquoteYAML strips the quotes the generator always writes, so a value can
// be compared against a plain string.
func unquoteYAML(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s)
}

func isReserved(name string) bool { return name == IndexFile || name == LogFile }

func conceptFiles(files map[string][]byte) int {
	n := 0
	for name := range files {
		if !isReserved(name) {
			n++
		}
	}
	return n
}

// sortedNames keeps the details of a failing check in a stable order, so the
// same defective bundle always reports the same message.
func sortedNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}

	// Reserved files first, then concepts in their numbered order - the same
	// reading order OrderedFiles uses.
	slices.Sort(names)
	ordered := make([]string, 0, len(names))
	for _, name := range []string{IndexFile, LogFile} {
		if _, ok := files[name]; ok {
			ordered = append(ordered, name)
		}
	}
	for _, name := range names {
		if !isReserved(name) {
			ordered = append(ordered, name)
		}
	}
	return ordered
}
