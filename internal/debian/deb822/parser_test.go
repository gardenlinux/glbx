package deb822

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestSingleStanza(t *testing.T) {
	input := "Package: hello\nVersion: 1.0-1\nArchitecture: amd64\n"
	r := NewReader(strings.NewReader(input))

	s, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s["package"] != "hello" {
		t.Errorf("package = %q, want %q", s["package"], "hello")
	}
	if s["version"] != "1.0-1" {
		t.Errorf("version = %q, want %q", s["version"], "1.0-1")
	}
	if s["architecture"] != "amd64" {
		t.Errorf("architecture = %q, want %q", s["architecture"], "amd64")
	}

	_, err = r.Next()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestMultipleStanzas(t *testing.T) {
	input := `Package: foo
Version: 1.0

Package: bar
Version: 2.0
`
	r := NewReader(strings.NewReader(input))

	s1, err := r.Next()
	if err != nil {
		t.Fatalf("stanza 1: unexpected error: %v", err)
	}
	if s1["package"] != "foo" {
		t.Errorf("stanza 1: package = %q, want %q", s1["package"], "foo")
	}
	if s1["version"] != "1.0" {
		t.Errorf("stanza 1: version = %q, want %q", s1["version"], "1.0")
	}

	s2, err := r.Next()
	if err != nil {
		t.Fatalf("stanza 2: unexpected error: %v", err)
	}
	if s2["package"] != "bar" {
		t.Errorf("stanza 2: package = %q, want %q", s2["package"], "bar")
	}
	if s2["version"] != "2.0" {
		t.Errorf("stanza 2: version = %q, want %q", s2["version"], "2.0")
	}

	_, err = r.Next()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestContinuationLines(t *testing.T) {
	input := `Package: hello
Description: A short description
 This is the long description
 that spans multiple lines.
 .
 And has paragraphs.
Version: 1.0
`
	r := NewReader(strings.NewReader(input))

	s, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedDesc := "A short description\n This is the long description\n that spans multiple lines.\n .\n And has paragraphs."
	if s["description"] != expectedDesc {
		t.Errorf("description = %q, want %q", s["description"], expectedDesc)
	}
	if s["version"] != "1.0" {
		t.Errorf("version = %q, want %q", s["version"], "1.0")
	}
}

func TestContinuationWithTab(t *testing.T) {
	input := "Package: test\nDescription: short\n\tcontinued with tab\nVersion: 2.0\n"
	r := NewReader(strings.NewReader(input))

	s, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedDesc := "short\n\tcontinued with tab"
	if s["description"] != expectedDesc {
		t.Errorf("description = %q, want %q", s["description"], expectedDesc)
	}
}

func TestCaseInsensitivity(t *testing.T) {
	input := "Package: test\nVERSION: 1.0\nArchitecture: amd64\n"
	r := NewReader(strings.NewReader(input))

	s, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s["package"] != "test" {
		t.Errorf("package = %q, want %q", s["package"], "test")
	}
	if s["version"] != "1.0" {
		t.Errorf("version = %q, want %q", s["version"], "1.0")
	}
	if s["architecture"] != "amd64" {
		t.Errorf("architecture = %q, want %q", s["architecture"], "amd64")
	}
}

func TestEmptyFieldValue(t *testing.T) {
	input := "Package: test\nDescription:\nVersion: 1.0\n"
	r := NewReader(strings.NewReader(input))

	s, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s["description"] != "" {
		t.Errorf("description = %q, want %q", s["description"], "")
	}
	if s["version"] != "1.0" {
		t.Errorf("version = %q, want %q", s["version"], "1.0")
	}
}

func TestComments(t *testing.T) {
	input := `# This is a comment
Package: hello
# Another comment
Version: 1.0
`
	r := NewReader(strings.NewReader(input))

	s, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s["package"] != "hello" {
		t.Errorf("package = %q, want %q", s["package"], "hello")
	}
	if s["version"] != "1.0" {
		t.Errorf("version = %q, want %q", s["version"], "1.0")
	}
}

func TestEmptyInput(t *testing.T) {
	r := NewReader(strings.NewReader(""))

	_, err := r.Next()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestOnlyEmptyLines(t *testing.T) {
	r := NewReader(strings.NewReader("\n\n\n"))

	_, err := r.Next()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestOnlyComments(t *testing.T) {
	r := NewReader(strings.NewReader("# comment1\n# comment2\n"))

	_, err := r.Next()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestTrailingNewlines(t *testing.T) {
	input := "Package: hello\nVersion: 1.0\n\n\n\n"
	r := NewReader(strings.NewReader(input))

	s, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s["package"] != "hello" {
		t.Errorf("package = %q, want %q", s["package"], "hello")
	}

	_, err = r.Next()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestNoTrailingNewline(t *testing.T) {
	input := "Package: hello\nVersion: 1.0"
	r := NewReader(strings.NewReader(input))

	s, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s["package"] != "hello" {
		t.Errorf("package = %q, want %q", s["package"], "hello")
	}
	if s["version"] != "1.0" {
		t.Errorf("version = %q, want %q", s["version"], "1.0")
	}
}

func TestRealWorldPackagesSnippet(t *testing.T) {
	data, err := os.ReadFile("testdata/real_world_snippet.txt")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	r := NewReader(strings.NewReader(string(data)))

	s1, err := r.Next()
	if err != nil {
		t.Fatalf("stanza 1: unexpected error: %v", err)
	}
	if s1["package"] != "apt" {
		t.Errorf("stanza 1: package = %q, want %q", s1["package"], "apt")
	}
	if s1["version"] != "2.6.1" {
		t.Errorf("stanza 1: version = %q, want %q", s1["version"], "2.6.1")
	}
	if s1["architecture"] != "amd64" {
		t.Errorf("stanza 1: architecture = %q, want %q", s1["architecture"], "amd64")
	}
	if s1["installed-size"] != "4321" {
		t.Errorf("stanza 1: installed-size = %q, want %q", s1["installed-size"], "4321")
	}
	// Check description has continuation
	if !strings.Contains(s1["description"], "commandline package manager") {
		t.Errorf("stanza 1: description missing short description")
	}
	if !strings.Contains(s1["description"], "apt-get for retrieval") {
		t.Errorf("stanza 1: description missing continuation content")
	}

	s2, err := r.Next()
	if err != nil {
		t.Fatalf("stanza 2: unexpected error: %v", err)
	}
	if s2["package"] != "bash" {
		t.Errorf("stanza 2: package = %q, want %q", s2["package"], "bash")
	}
	if s2["pre-depends"] != "libc6 (>= 2.36), libtinfo6 (>= 6)" {
		t.Errorf("stanza 2: pre-depends = %q", s2["pre-depends"])
	}

	_, err = r.Next()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestInvalidFieldName(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"space in name", "Bad Name: value\n"},
		{"non-ascii in name", "Bad\x80Name: value\n"},
		{"empty field name", ": value\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewReader(strings.NewReader(tt.input))
			_, err := r.Next()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestColonInValue(t *testing.T) {
	// "Bad:Name: value" splits at first colon -> field "Bad", value "Name: value"
	// This is correct deb822 behavior
	input := "Bad:Name: value\n"
	r := NewReader(strings.NewReader(input))
	s, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "Bad" is the field name (normalized to lowercase)
	if s["bad"] != "Name: value" {
		t.Errorf("bad = %q, want %q", s["bad"], "Name: value")
	}
}

func TestContinuationBeforeField(t *testing.T) {
	input := " continuation without field\n"
	r := NewReader(strings.NewReader(input))

	_, err := r.Next()
	if err == nil {
		t.Fatal("expected error for continuation before any field")
	}
}

func TestMultipleEmptyLinesBetweenStanzas(t *testing.T) {
	input := "Package: foo\nVersion: 1.0\n\n\n\nPackage: bar\nVersion: 2.0\n"
	r := NewReader(strings.NewReader(input))

	s1, err := r.Next()
	if err != nil {
		t.Fatalf("stanza 1: unexpected error: %v", err)
	}
	if s1["package"] != "foo" {
		t.Errorf("stanza 1: package = %q, want %q", s1["package"], "foo")
	}

	s2, err := r.Next()
	if err != nil {
		t.Fatalf("stanza 2: unexpected error: %v", err)
	}
	if s2["package"] != "bar" {
		t.Errorf("stanza 2: package = %q, want %q", s2["package"], "bar")
	}

	_, err = r.Next()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestFieldValueWithColons(t *testing.T) {
	input := "Package: test\nDepends: libc6 (>= 2.34), pkg:amd64\n"
	r := NewReader(strings.NewReader(input))

	s, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s["depends"] != "libc6 (>= 2.34), pkg:amd64" {
		t.Errorf("depends = %q, want %q", s["depends"], "libc6 (>= 2.34), pkg:amd64")
	}
}

func TestEmptyFieldWithContinuation(t *testing.T) {
	input := "Package: test\nDescription:\n Long description starts here\n second line\nVersion: 1.0\n"
	r := NewReader(strings.NewReader(input))

	s, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedDesc := "\n Long description starts here\n second line"
	if s["description"] != expectedDesc {
		t.Errorf("description = %q, want %q", s["description"], expectedDesc)
	}
}
