package bundle

import (
	"fmt"
	"strings"
	"time"
)

// OKF v0.1 asks exactly one thing of every concept: a `type` field in its
// YAML frontmatter. Everything else is optional, but the spec names a small
// set of fields consumers can rely on being queryable - type, title,
// description, resource, tags, timestamp - so the generator fills in the
// ones it can actually know.
//
// These are the types this generator emits. index.md and log.md are reserved
// filenames in OKF, and get types matching their role.
const (
	TypeConcept = "concept"
	TypeIndex   = "index"
	TypeLog     = "log"
)

// frontmatter is the YAML block at the top of every file in the bundle.
// Field order is fixed rather than map-ordered so two conversions of the
// same document produce byte-identical files.
type frontmatter struct {
	Type        string
	Title       string
	Description string
	Tags        []string
	Timestamp   time.Time
	// Source names the document this file was derived from. Not one of OKF's
	// standard fields, but the single most useful thing to know about a
	// generated concept, and the spec leaves the rest of the content model
	// to the producer.
	Source string
}

// render writes the frontmatter as a YAML block delimited by '---', the form
// OKF specifies.
func (f frontmatter) render() string {
	var b strings.Builder

	b.WriteString("---\n")
	fmt.Fprintf(&b, "type: %s\n", yamlString(f.Type))

	if f.Title != "" {
		fmt.Fprintf(&b, "title: %s\n", yamlString(f.Title))
	}
	if f.Description != "" {
		fmt.Fprintf(&b, "description: %s\n", yamlString(f.Description))
	}
	if len(f.Tags) > 0 {
		quoted := make([]string, 0, len(f.Tags))
		for _, tag := range f.Tags {
			quoted = append(quoted, yamlString(tag))
		}
		fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(quoted, ", "))
	}
	if f.Source != "" {
		fmt.Fprintf(&b, "source: %s\n", yamlString(f.Source))
	}
	if !f.Timestamp.IsZero() {
		fmt.Fprintf(&b, "timestamp: %s\n", f.Timestamp.UTC().Format(time.RFC3339))
	}

	b.WriteString("---\n\n")
	return b.String()
}

// yamlString always quotes, so a title containing a colon, a leading '#', or
// anything else YAML would read as syntax stays a plain string.
func yamlString(s string) string {
	escaped := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", " ",
		"\r", "",
		"\t", " ",
	).Replace(s)

	return `"` + escaped + `"`
}
