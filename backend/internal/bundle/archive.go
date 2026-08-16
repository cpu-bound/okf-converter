package bundle

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"time"
)

// Zip packages the bundle as a single downloadable archive. Every entry is
// nested under the bundle root, so extracting it yields one self-contained
// folder instead of loose files in whatever directory the user happened to
// be in.
//
// Entries are written index.md, log.md, then the concepts in document
// order - the order someone opening the archive wants to read them in, and
// deterministic, so re-converting the same document twice produces the same
// listing.
func (b Bundle) Zip() ([]byte, error) {
	var buf bytes.Buffer
	if err := b.WriteZip(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteZip streams the archive into w. Bundles are small enough to hold in
// memory (they are Markdown), but writing through an io.Writer means the
// caller picks - a buffer, an HTTP response, an upload - rather than the
// bundle forcing a full copy on everyone.
func (b Bundle) WriteZip(w io.Writer) error {
	zw := zip.NewWriter(w)

	for _, f := range b.OrderedFiles() {
		header := &zip.FileHeader{
			Name:   b.Root + "/" + f.Name,
			Method: zip.Deflate,
			// A fixed timestamp keeps the archive byte-identical across runs
			// of the same conversion, which is what makes a redelivered job
			// overwrite its result with the same bytes instead of a
			// gratuitously different archive.
			Modified: time.Unix(0, 0).UTC(),
		}

		entry, err := zw.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("create zip entry %q: %w", f.Name, err)
		}
		if _, err := entry.Write(f.Content); err != nil {
			return fmt.Errorf("write zip entry %q: %w", f.Name, err)
		}
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf("close zip writer: %w", err)
	}
	return nil
}

// OrderedFiles returns the bundle's files in reading order: index.md,
// log.md, then the concepts as they appear in the source document. Callers
// that persist or package the bundle use it so storage listings, the archive
// and the database rows all agree on one order.
func (b Bundle) OrderedFiles() []File {
	ordered := make([]File, 0, len(b.Files))

	for _, name := range []string{IndexFile, LogFile} {
		if content, ok := b.Find(name); ok {
			ordered = append(ordered, File{Name: name, Content: content})
		}
	}

	for _, f := range b.Files {
		if f.Name != IndexFile && f.Name != LogFile {
			ordered = append(ordered, f)
		}
	}

	return ordered
}
