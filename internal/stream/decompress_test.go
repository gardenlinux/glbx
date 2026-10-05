package stream

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"
)

// compressData uses an external tool to compress test data.
func compressData(t *testing.T, data []byte, tool string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(tool, args...)
	cmd.Stdin = bytes.NewReader(data)
	var out bytes.Buffer
	cmd.Stdout = &out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("compressing with %s: %v\nstderr: %s", tool, err, stderr.String())
	}
	return out.Bytes()
}

func TestXZDecompress(t *testing.T) {
	original := "The quick brown fox jumps over the lazy dog.\n"
	compressed := compressData(t, []byte(original), "xz", "-c")

	rc, err := XZDecompress(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("XZDecompress: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, rc)
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := buf.String(); got != original {
		t.Errorf("got %q, want %q", got, original)
	}
}

func TestGzipDecompress(t *testing.T) {
	original := "Gzip compressed test data with special chars: äöü€\n"
	compressed := compressData(t, []byte(original), "gzip", "-c")

	rc, err := GzipDecompress(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("GzipDecompress: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, rc)
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := buf.String(); got != original {
		t.Errorf("got %q, want %q", got, original)
	}
}

func TestBzip2Decompress(t *testing.T) {
	original := "Bzip2 compressed test data\nwith multiple lines\n"
	compressed := compressData(t, []byte(original), "bzip2", "-c")

	rc, err := Bzip2Decompress(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("Bzip2Decompress: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, rc)
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := buf.String(); got != original {
		t.Errorf("got %q, want %q", got, original)
	}
}

func TestZstdDecompress(t *testing.T) {
	original := "Zstandard compressed data for testing\n"
	compressed := compressData(t, []byte(original), "zstd", "-c")

	rc, err := ZstdDecompress(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("ZstdDecompress: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, rc)
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := buf.String(); got != original {
		t.Errorf("got %q, want %q", got, original)
	}
}

func TestAutoDecompress(t *testing.T) {
	original := "Auto-detection test data\n"

	tests := []struct {
		filename string
		tool     string
		args     []string
	}{
		{"archive.tar.xz", "xz", []string{"-c"}},
		{"archive.tar.gz", "gzip", []string{"-c"}},
		{"archive.tar.bz2", "bzip2", []string{"-c"}},
		{"archive.tar.zst", "zstd", []string{"-c"}},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			compressed := compressData(t, []byte(original), tt.tool, tt.args...)

			rc, err := AutoDecompress(bytes.NewReader(compressed), tt.filename)
			if err != nil {
				t.Fatalf("AutoDecompress(%s): %v", tt.filename, err)
			}

			var buf bytes.Buffer
			io.Copy(&buf, rc)
			if err := rc.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			if got := buf.String(); got != original {
				t.Errorf("got %q, want %q", got, original)
			}
		})
	}
}

func TestAutoDecompress_UnsupportedExtension(t *testing.T) {
	_, err := AutoDecompress(strings.NewReader("data"), "file.lz4")
	if err == nil {
		t.Fatal("expected error for unsupported extension, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error should mention 'unsupported': %v", err)
	}
}

func TestAutoDecompress_PathLikeFilenames(t *testing.T) {
	original := "path test\n"

	// Should work with full path-like filenames
	compressed := compressData(t, []byte(original), "gzip", "-c")
	rc, err := AutoDecompress(bytes.NewReader(compressed), "/some/path/to/file.gz")
	if err != nil {
		t.Fatalf("AutoDecompress with path: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, rc)
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := buf.String(); got != original {
		t.Errorf("got %q, want %q", got, original)
	}
}

func TestDecompress_InvalidData(t *testing.T) {
	// Feed invalid data to decompressor — should fail when reading or closing
	invalidData := bytes.NewReader([]byte("this is not compressed data"))

	rc, err := GzipDecompress(invalidData)
	if err != nil {
		// Some implementations might fail on start — that's ok
		return
	}

	_, readErr := io.Copy(io.Discard, rc)
	closeErr := rc.Close()

	// At least one of read or close should error
	if readErr == nil && closeErr == nil {
		t.Fatal("expected error when decompressing invalid data")
	}
}

func TestDecompress_EmptyInput(t *testing.T) {
	// Empty input to gzip should fail (no valid gzip stream)
	rc, err := GzipDecompress(bytes.NewReader(nil))
	if err != nil {
		return // acceptable
	}

	_, readErr := io.Copy(io.Discard, rc)
	closeErr := rc.Close()

	if readErr == nil && closeErr == nil {
		t.Fatal("expected error when decompressing empty input")
	}
}

func TestDecompress_LargeData(t *testing.T) {
	// Test with a larger payload
	original := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit.\n", 10000)
	compressed := compressData(t, []byte(original), "gzip", "-c")

	rc, err := GzipDecompress(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("GzipDecompress: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, rc)
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if buf.Len() != len(original) {
		t.Errorf("output length %d, want %d", buf.Len(), len(original))
	}
}
