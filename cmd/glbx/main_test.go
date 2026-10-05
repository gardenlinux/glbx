package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireBin returns the pre-built glbx binary path via GLBX_BIN. Tests never
// invoke `go build`; the Makefile builds the binary once and exports GLBX_BIN.
func requireBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("GLBX_BIN")
	if bin == "" {
		t.Skip("GLBX_BIN not set; run via `make test`")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("GLBX_BIN=%q does not exist; run `make build` first", bin)
	}
	return bin
}

func run(t *testing.T, env []string, args ...string) ([]byte, error) {
	t.Helper()
	bin := requireBin(t)
	cmd := exec.Command(bin, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	return cmd.CombinedOutput()
}

func TestCLINoArgsExitsNonZero(t *testing.T) {
	if _, err := run(t, nil); err == nil {
		t.Fatal("expected non-zero exit when invoked with no args")
	}
}

func TestCLIHelp(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		t.Run(arg, func(t *testing.T) {
			out, err := run(t, nil, arg)
			if err != nil {
				t.Fatalf("glbx %s: %v\n%s", arg, err, out)
			}
			if !strings.Contains(string(out), "usage:") {
				t.Errorf("expected 'usage:' in %s output, got: %s", arg, out)
			}
			for _, sub := range []string{"build", "cache", "import", "graph"} {
				if !strings.Contains(string(out), sub) {
					t.Errorf("help should mention %q, got:\n%s", sub, out)
				}
			}
		})
	}
}

func TestCLIUnknownCommand(t *testing.T) {
	out, err := run(t, nil, "totally-unknown-cmd")
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
	if !strings.Contains(string(out), "unknown command") {
		t.Errorf("expected 'unknown command' in output, got: %s", out)
	}
}

func TestCLIStatus(t *testing.T) {
	dir := t.TempDir()
	out, err := run(t, []string{"GLBX_CACHE=" + dir}, "status")
	if err != nil {
		t.Fatalf("glbx status: %v\n%s", err, out)
	}
	if len(out) == 0 {
		t.Fatal("expected status output")
	}
}

func TestCLICacheStatusEmpty(t *testing.T) {
	dir := t.TempDir()
	out, err := run(t, []string{"GLBX_CACHE=" + dir}, "cache", "status")
	if err != nil {
		t.Fatalf("glbx cache status: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "blobs:") {
		t.Errorf("cache status output should mention blobs:\n%s", out)
	}
}

func TestCLICacheUnknownSubcommand(t *testing.T) {
	dir := t.TempDir()
	out, err := run(t, []string{"GLBX_CACHE=" + dir}, "cache", "bogus")
	if err == nil {
		t.Fatal("expected error for unknown cache subcommand")
	}
	if !strings.Contains(string(out), "unknown cache subcommand") {
		t.Errorf("expected 'unknown cache subcommand' in output, got:\n%s", out)
	}
}

func TestCLICacheNoSubcommand(t *testing.T) {
	dir := t.TempDir()
	out, err := run(t, []string{"GLBX_CACHE=" + dir}, "cache")
	if err == nil {
		t.Fatal("expected error when `cache` is called with no subcommand")
	}
	if !strings.Contains(string(out), "usage:") {
		t.Errorf("expected usage message, got:\n%s", out)
	}
}

func TestCLICacheBlobsStoreAndGet(t *testing.T) {
	dir := t.TempDir()
	env := []string{"GLBX_CACHE=" + dir}

	contentPath := filepath.Join(dir, "payload.txt")
	if err := os.WriteFile(contentPath, []byte("hello-cache-roundtrip\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, env, "cache", "blobs", "store", contentPath)
	if err != nil {
		t.Fatalf("cache blobs store: %v\n%s", err, out)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		t.Fatalf("expected hash output, got %q", out)
	}
	hash := fields[len(fields)-1]
	if len(hash) != 64 {
		t.Fatalf("expected 64-hex-char hash, got %q", hash)
	}

	if out, err := run(t, env, "cache", "blobs", "check", hash); err != nil {
		t.Fatalf("cache blobs check %s: %v\n%s", hash, err, out)
	}

	out, err = run(t, env, "cache", "blobs", "get", hash)
	if err != nil {
		t.Fatalf("cache blobs get %s: %v\n%s", hash, err, out)
	}
	if !strings.Contains(string(out), "hello-cache-roundtrip") {
		t.Fatalf("expected stored content back, got:\n%s", out)
	}
}

func TestCLICacheBlobsCheckMissing(t *testing.T) {
	dir := t.TempDir()
	env := []string{"GLBX_CACHE=" + dir}
	missing := strings.Repeat("0", 64)
	out, err := run(t, env, "cache", "blobs", "check", missing)
	if err != nil {
		t.Fatalf("cache blobs check (missing): %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "not found") {
		t.Errorf("expected 'not found' for missing blob, got:\n%s", out)
	}
}

func TestCLICacheMapRoundTrip(t *testing.T) {
	dir := t.TempDir()
	env := []string{"GLBX_CACHE=" + dir}

	// Store a blob, map a key to it, read it back, and list it.
	payload := filepath.Join(dir, "p.txt")
	os.WriteFile(payload, []byte("map-target"), 0644)
	out, _ := run(t, env, "cache", "blobs", "store", payload)
	f := strings.Fields(strings.TrimSpace(string(out)))
	blob := f[len(f)-1]
	key := strings.Repeat("a", 64)

	if out, err := run(t, env, "cache", "map", "set", key, blob); err != nil {
		t.Fatalf("cache map set: %v\n%s", err, out)
	}
	out, err := run(t, env, "cache", "map", "get", key)
	if err != nil {
		t.Fatalf("cache map get: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), blob) {
		t.Errorf("map get should return %s, got:\n%s", blob, out)
	}
}

func TestCLIImportRequiresArg(t *testing.T) {
	if _, err := run(t, nil, "import"); err == nil {
		t.Fatal("expected error when `import` is called with no args")
	}
}
