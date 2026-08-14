package storage

import (
	"strings"
	"testing"
)

func TestCreateObjectName(t *testing.T) {
	tests := []struct {
		name         string
		originalName string
		wantSuffix   string
		wantHasExt   bool
	}{
		{"simple extension", "photo.jpg", ".jpg", true},
		{"uppercase extension lowercased", "Document.PDF", ".pdf", true},
		{"no extension", "README", "", false},
		{"trailing dot", "archive.", "", false},
		{"multiple dots uses last segment", "archive.tar.gz", ".gz", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CreateObjectName("user-123", tt.originalName)

			if !strings.HasPrefix(got, "user-123/") {
				t.Errorf("CreateObjectName() = %q, want prefix %q", got, "user-123/")
			}

			if tt.wantHasExt && !strings.HasSuffix(got, tt.wantSuffix) {
				t.Errorf("CreateObjectName() = %q, want suffix %q", got, tt.wantSuffix)
			}

			if !tt.wantHasExt && strings.Contains(strings.TrimPrefix(got, "user-123/"), ".") {
				t.Errorf("CreateObjectName() = %q, want no extension", got)
			}
		})
	}
}
