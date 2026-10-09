package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/gardenlinux/glbx/internal/importer"
)

// TestPrintImportResult asserts the --no-merge JSON surface: a committed import
// emits its pkg/version/commit, and an already-present no-op emits an empty
// commit with already_present true, which a driver reads as "nothing to merge".
func TestPrintImportResult(t *testing.T) {
	cases := []struct {
		name   string
		result importer.ImportResult
		want   importJSON
	}{
		{
			name: "first import",
			result: importer.ImportResult{
				Name:        "glibc",
				Version:     "2.41-1",
				CommitHash:  "abc123",
				FirstImport: true,
			},
			want: importJSON{Pkg: "glibc", Version: "2.41-1", Commit: "abc123", FirstImport: true},
		},
		{
			name: "subsequent import",
			result: importer.ImportResult{
				Name:       "glibc",
				Version:    "2.41-2",
				CommitHash: "def456",
			},
			want: importJSON{Pkg: "glibc", Version: "2.41-2", Commit: "def456"},
		},
		{
			name: "already present",
			result: importer.ImportResult{
				Name:           "glibc",
				Version:        "2.41-1",
				AlreadyPresent: true,
			},
			want: importJSON{Pkg: "glibc", Version: "2.41-1", AlreadyPresent: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := printImportResult(&buf, &tc.result); err != nil {
				t.Fatalf("printImportResult: %v", err)
			}
			var got importJSON
			if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
				t.Fatalf("output is not valid JSON (%v): %q", err, buf.String())
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
