package stream

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestProcReadCloser_Success(t *testing.T) {
	// Use 'cat' to echo back input through a process
	input := "hello, world!\n"
	prc, err := newProcReadCloser(strings.NewReader(input), "cat")
	if err != nil {
		t.Fatalf("newProcReadCloser: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, prc); err != nil {
		t.Fatalf("reading from proc: %v", err)
	}

	if err := prc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := buf.String(); got != input {
		t.Errorf("got %q, want %q", got, input)
	}
}

func TestProcReadCloser_LargeData(t *testing.T) {
	// Test with a larger payload to exercise buffering
	input := strings.Repeat("abcdefghijklmnopqrstuvwxyz\n", 10000)
	prc, err := newProcReadCloser(strings.NewReader(input), "cat")
	if err != nil {
		t.Fatalf("newProcReadCloser: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, prc); err != nil {
		t.Fatalf("reading from proc: %v", err)
	}

	if err := prc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if buf.Len() != len(input) {
		t.Errorf("output length %d, want %d", buf.Len(), len(input))
	}
}

func TestProcReadCloser_NonZeroExit(t *testing.T) {
	// 'false' always exits with status 1
	prc, err := newProcReadCloser(strings.NewReader(""), "false")
	if err != nil {
		t.Fatalf("newProcReadCloser: %v", err)
	}

	// Drain any output
	io.Copy(io.Discard, prc)

	err = prc.Close()
	if err == nil {
		t.Fatal("expected error from Close() on non-zero exit, got nil")
	}
}

func TestProcReadCloser_StderrInError(t *testing.T) {
	// Use sh -c to produce stderr output and exit non-zero
	prc, err := newProcReadCloser(strings.NewReader(""), "sh", "-c", "echo oops >&2; exit 1")
	if err != nil {
		t.Fatalf("newProcReadCloser: %v", err)
	}

	io.Copy(io.Discard, prc)

	err = prc.Close()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Errorf("error should contain stderr content 'oops', got: %v", err)
	}
}

func TestProcReadCloser_InvalidCommand(t *testing.T) {
	_, err := newProcReadCloser(strings.NewReader(""), "nonexistent_command_xyz_123")
	if err == nil {
		t.Fatal("expected error for nonexistent command, got nil")
	}
}

func TestProcReadCloser_Transform(t *testing.T) {
	// Use 'tr' to transform data through the process
	input := "hello world"
	prc, err := newProcReadCloser(strings.NewReader(input), "tr", "a-z", "A-Z")
	if err != nil {
		t.Fatalf("newProcReadCloser: %v", err)
	}

	var buf bytes.Buffer
	io.Copy(&buf, prc)

	if err := prc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := buf.String(); got != "HELLO WORLD" {
		t.Errorf("got %q, want %q", got, "HELLO WORLD")
	}
}

func TestProcReadCloser_DoubleClose(t *testing.T) {
	prc, err := newProcReadCloser(strings.NewReader("test"), "cat")
	if err != nil {
		t.Fatalf("newProcReadCloser: %v", err)
	}

	io.Copy(io.Discard, prc)

	if err := prc.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second close should be a no-op
	if err := prc.Close(); err != nil {
		t.Fatalf("second Close should be nil: %v", err)
	}
}
