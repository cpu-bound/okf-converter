package bundle

import (
	"strings"
	"testing"
)

// validBundle is what the generator produces for an ordinary document: the
// baseline every corruption below is measured against.
func validBundle(t *testing.T) Bundle {
	t.Helper()
	return build(t, []Unit{
		{Title: "Introducción", Body: "primer párrafo"},
		{Title: "Metodología", Body: "segundo párrafo"},
	})
}

// replace swaps one file's content, so a test can corrupt exactly one
// property of an otherwise well-formed bundle.
func replace(t *testing.T, b *Bundle, name string, content string) {
	t.Helper()
	for i := range b.Files {
		if b.Files[i].Name == name {
			b.Files[i].Content = []byte(content)
			return
		}
	}
	t.Fatalf("bundle has no file named %q", name)
}

func remove(t *testing.T, b *Bundle, name string) {
	t.Helper()
	kept := b.Files[:0]
	found := false
	for _, f := range b.Files {
		if f.Name == name {
			found = true
			continue
		}
		kept = append(kept, f)
	}
	if !found {
		t.Fatalf("bundle has no file named %q", name)
	}
	b.Files = kept
}

func check(t *testing.T, r Report, id string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("report has no check %q", id)
	return Check{}
}

// The generator's own output has to pass every rule, warnings included -
// otherwise the validator is measuring something we never produce.
func TestGeneratedBundleIsValid(t *testing.T) {
	r := Validate(validBundle(t))

	if r.Verdict != VerdictValid {
		t.Errorf("Verdict = %q, want %q", r.Verdict, VerdictValid)
	}
	if r.Platform != VerdictValid || r.OKF != VerdictValid {
		t.Errorf("Platform = %q, OKF = %q, want both %q", r.Platform, r.OKF, VerdictValid)
	}

	for _, c := range r.Checks {
		if !c.Passed {
			t.Errorf("check %q failed on a well-formed bundle: %v", c.ID, c.Details)
		}
	}
	if !r.Verdict.Publishable() {
		t.Error("a well-formed bundle was classified as unpublishable")
	}
}

// §6 names this case explicitly: without index.md or log.md the bundle is not
// published and no download is enabled.
func TestMissingReservedFileMakesBundleInvalid(t *testing.T) {
	for _, name := range []string{IndexFile, LogFile} {
		t.Run(name, func(t *testing.T) {
			b := validBundle(t)
			remove(t, &b, name)

			r := Validate(b)
			if r.Verdict != VerdictInvalid {
				t.Errorf("Verdict = %q, want %q", r.Verdict, VerdictInvalid)
			}
			if r.Verdict.Publishable() {
				t.Error("a bundle missing a required file was classified as publishable")
			}

			c := check(t, r, "minimum-structure")
			if c.Passed {
				t.Fatal("minimum-structure passed with a required file missing")
			}
			if !strings.Contains(strings.Join(c.Details, " "), name) {
				t.Errorf("details do not name the missing file: %v", c.Details)
			}
		})
	}
}

func TestBundleWithNoConceptsIsInvalid(t *testing.T) {
	b := validBundle(t)

	var concepts []string
	for _, f := range b.Files {
		if f.Name != IndexFile && f.Name != LogFile {
			concepts = append(concepts, f.Name)
		}
	}
	for _, name := range concepts {
		remove(t, &b, name)
	}

	r := Validate(b)
	if check(t, r, "minimum-structure").Passed {
		t.Error("minimum-structure passed on a bundle with no concepts")
	}
	if r.Verdict != VerdictInvalid {
		t.Errorf("Verdict = %q, want %q", r.Verdict, VerdictInvalid)
	}
}

// index.md is fully generated, so a link in it that does not resolve is a
// defect of ours - the bundle is unusable from its own entry point.
func TestUnresolvedIndexLinkMakesBundleInvalid(t *testing.T) {
	b := validBundle(t)
	index, _ := b.Find(IndexFile)
	replace(t, &b, IndexFile, strings.Replace(string(index), "01-introduccion.md", "99-inexistente.md", 1))

	r := Validate(b)
	if r.Verdict != VerdictInvalid {
		t.Errorf("Verdict = %q, want %q", r.Verdict, VerdictInvalid)
	}

	c := check(t, r, "index-links")
	if c.Passed {
		t.Fatal("index-links passed with a dangling link")
	}
	if !strings.Contains(strings.Join(c.Details, " "), "99-inexistente.md") {
		t.Errorf("details do not name the dangling target: %v", c.Details)
	}
}

// The mirror defect: a concept that exists but is unreachable from the index.
func TestOrphanConceptMakesBundleInvalid(t *testing.T) {
	b := validBundle(t)
	b.Files = append(b.Files, File{
		Name:    "03-huerfano.md",
		Content: []byte("---\ntype: \"concept\"\ntitle: \"Huérfano\"\ndescription: \"x\"\ntags: [\"okf\"]\ntimestamp: 2026-08-16T00:00:00Z\n---\n\n# Huérfano\n"),
	})

	r := Validate(b)
	c := check(t, r, "concepts-indexed")
	if c.Passed {
		t.Fatal("concepts-indexed passed with an unreachable concept")
	}
	if r.Verdict != VerdictInvalid {
		t.Errorf("Verdict = %q, want %q", r.Verdict, VerdictInvalid)
	}
}

// A relative link the source author wrote to a file outside the bundle is a
// real defect to report, but not one worth refusing to publish over: the
// bundle stays usable, and the user is told.
func TestInheritedBrokenLinkOnlyWarns(t *testing.T) {
	b := build(t, []Unit{{Title: "Uno", Body: "ver [el anexo](anexo-b.md) para el detalle"}})

	r := Validate(b)
	if r.Verdict != VerdictWithWarnings {
		t.Fatalf("Verdict = %q, want %q", r.Verdict, VerdictWithWarnings)
	}
	if !r.Verdict.Publishable() {
		t.Error("a bundle with only warnings must still be publishable")
	}

	c := check(t, r, "concept-links")
	if c.Passed || c.Severity != SeverityWarning {
		t.Errorf("concept-links = %+v, want a failed warning", c)
	}
}

// A heading containing brackets gets escaped in the index's link text. The
// validator has to read those links the same way the generator writes them,
// or it would report a perfectly good concept as unreachable and refuse to
// publish the bundle.
func TestBracketsInTitlesDoNotBreakValidation(t *testing.T) {
	b := build(t, []Unit{
		{Title: "Anexo [borrador]", Body: "x"},
		{Title: "Cierre", Body: "y"},
	})

	if r := Validate(b); r.Verdict != VerdictValid {
		t.Errorf("Verdict = %q, want %q: %v", r.Verdict, VerdictValid, r.Failures())
	}
}

// Links that point outside the bundle are not ours to resolve.
func TestExternalLinksAreNotValidated(t *testing.T) {
	b := build(t, []Unit{{Title: "Uno", Body: "ver [la spec](https://example.com/okf), [arriba](#seccion) y [raíz](/docs/x.md)"}})

	if r := Validate(b); r.Verdict != VerdictValid {
		t.Errorf("Verdict = %q, want %q (external links must be ignored): %v", r.Verdict, VerdictValid, r.Failures())
	}
}

func TestMissingFrontmatterBreaksOKFConformance(t *testing.T) {
	b := validBundle(t)
	replace(t, &b, "01-introduccion.md", "# Introducción\n\nsin frontmatter\n")

	r := Validate(b)
	if r.OKF != VerdictInvalid {
		t.Errorf("OKF = %q, want %q", r.OKF, VerdictInvalid)
	}
	if check(t, r, "okf-frontmatter").Passed {
		t.Error("okf-frontmatter passed on a file with no frontmatter")
	}
	if check(t, r, "okf-type").Passed {
		t.Error("okf-type passed on a file with no frontmatter")
	}
}

// The two dimensions are reported separately (§5.2): a bundle can be a
// perfectly usable bundle for this platform and still fall short of OKF.
func TestPlatformValidityAndOKFConformanceAreSeparate(t *testing.T) {
	b := validBundle(t)
	replace(t, &b, "01-introduccion.md",
		"---\ntype: \"concept\"\n---\n\n# Introducción\n\nprimer párrafo\n")

	r := Validate(b)
	if r.Platform != VerdictValid {
		t.Errorf("Platform = %q, want %q - structure and links are intact", r.Platform, VerdictValid)
	}
	if r.OKF != VerdictWithWarnings {
		t.Errorf("OKF = %q, want %q - the standard fields are missing", r.OKF, VerdictWithWarnings)
	}
	if r.Verdict != VerdictWithWarnings {
		t.Errorf("Verdict = %q, want the worse of the two", r.Verdict)
	}
}

func TestReservedFilesMustDeclareTheirRole(t *testing.T) {
	b := validBundle(t)
	index, _ := b.Find(IndexFile)
	replace(t, &b, IndexFile, strings.Replace(string(index), `type: "index"`, `type: "concept"`, 1))

	r := Validate(b)
	c := check(t, r, "okf-reserved-types")
	if c.Passed {
		t.Fatal("okf-reserved-types passed with index.md declaring the wrong type")
	}
	if r.OKF != VerdictWithWarnings {
		t.Errorf("OKF = %q, want %q", r.OKF, VerdictWithWarnings)
	}
}

// The verdict has to reach log.md: §3 asks the log to record the validations
// the bundle went through, including the ones it passed.
func TestValidationVerdictReachesTheLog(t *testing.T) {
	b := validBundle(t)
	b.SetValidation(Validate(b))

	log, ok := b.Find(LogFile)
	if !ok {
		t.Fatal("bundle is missing log.md")
	}

	for _, want := range []string{
		"## Validación",
		"**Resultado:** válido",
		"Conformidad OKF",
		"El bundle contiene index.md, log.md y al menos un concepto",
		"superada",
	} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log.md does not mention %q:\n%s", want, log)
		}
	}
}

func TestFailedRulesAreListedInTheLog(t *testing.T) {
	b := build(t, []Unit{{Title: "Uno", Body: "ver [el anexo](anexo-b.md)"}})
	b.SetValidation(Validate(b))

	log, _ := b.Find(LogFile)
	for _, want := range []string{"### Hallazgos", "anexo-b.md", "advertencia"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log.md does not report the failing rule (%q):\n%s", want, log)
		}
	}
}

func TestSummaryNamesTheRulesThatFailed(t *testing.T) {
	b := validBundle(t)
	remove(t, &b, LogFile)

	summary := Validate(b).Summary()
	if !strings.Contains(summary, "inválido") {
		t.Errorf("Summary() = %q, want it to state the verdict", summary)
	}
	if !strings.Contains(summary, "index.md, log.md") {
		t.Errorf("Summary() = %q, want it to name the rule that failed", summary)
	}
}

// Failures are ordered worst first, so the summary leads with the reason the
// bundle was refused rather than with a warning that did not matter.
func TestFailuresPutErrorsBeforeWarnings(t *testing.T) {
	b := build(t, []Unit{{Title: "Uno", Body: "ver [el anexo](anexo-b.md)"}})
	remove(t, &b, IndexFile)

	failures := Validate(b).Failures()
	if len(failures) < 2 {
		t.Fatalf("Failures() = %d, want at least an error and a warning", len(failures))
	}
	if failures[0].Severity != SeverityError {
		t.Errorf("Failures()[0].Severity = %q, want %q", failures[0].Severity, SeverityError)
	}
	if failures[len(failures)-1].Severity != SeverityWarning {
		t.Errorf("last failure severity = %q, want %q", failures[len(failures)-1].Severity, SeverityWarning)
	}
}

func TestRelativeLinks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"simple", "[a](01-uno.md)", []string{"01-uno.md"}},
		{"dot slash", "[a](./01-uno.md)", []string{"01-uno.md"}},
		{"fragment", "[a](01-uno.md#seccion)", []string{"01-uno.md"}},
		{"anchor only", "[a](#seccion)", nil},
		{"absolute url", "[a](https://example.com/x.md)", nil},
		{"absolute path", "[a](/x.md)", nil},
		{"mailto", "[a](mailto:x@y.z)", nil},
		{"escaped text", `[texto \[con\] corchetes](01-uno.md)`, []string{"01-uno.md"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := relativeLinks([]byte(tt.in))
			if len(got) != len(tt.want) {
				t.Fatalf("relativeLinks(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("relativeLinks(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}
