package stream

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTarCreateDeterministic verifies that TarCreate produces byte-identical
// output when run twice on the same directory contents, regardless of when the
// files were created. This requires timestamp normalization (--mtime=@0).
//
// Per the architecture blueprint: "After chroot assembly, reset all file
// timestamps to epoch 0 for deterministic hashing." TarCreate is used to
// pack the rootfs into a tarball for the object store — if timestamps are
// not normalized, the same rootfs content produces different tar bytes,
// breaking the content-addressed cache.
func TestTarCreateDeterministic(t *testing.T) {
	// Create a directory with known content
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "etc"), 0755)
	os.WriteFile(filepath.Join(dir, "etc", "hostname"), []byte("test\n"), 0644)
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("world\n"), 0644)

	// Create tar archive (first time)
	var buf1 bytes.Buffer
	if err := TarCreate(dir, &buf1); err != nil {
		t.Fatal(err)
	}

	// Explicitly change the mtime on a file (simulating creation at different time)
	futureTime := time.Now().Add(24 * time.Hour)
	os.Chtimes(filepath.Join(dir, "hello.txt"), futureTime, futureTime)
	os.Chtimes(filepath.Join(dir, "etc", "hostname"), futureTime, futureTime)

	// Create tar archive (second time, after mtime change)
	var buf2 bytes.Buffer
	if err := TarCreate(dir, &buf2); err != nil {
		t.Fatal(err)
	}

	// The archives MUST be byte-identical for deterministic caching.
	// If TarCreate doesn't use --mtime=@0 (or equivalent normalization),
	// the two archives will differ because tar records file timestamps.
	if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Fatalf("TarCreate is NOT deterministic: changing file mtimes produces different tarballs.\n"+
			"  The tar command must use --mtime=@0 (or equivalent) to normalize timestamps.\n"+
			"  Without this, the same rootfs content maps to different blob hashes in the object store,\n"+
			"  breaking the content-addressed caching guarantee.\n"+
			"  Archive 1 size: %d bytes, Archive 2 size: %d bytes",
			buf1.Len(), buf2.Len())
	}
}

// TestTarCreateTimestampsAreEpoch verifies that all entries in the tar have
// their modification time set to Unix epoch (1970-01-01 00:00:00 UTC).
func TestTarCreateTimestampsAreEpoch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "subdir"), 0755)
	os.WriteFile(filepath.Join(dir, "subdir", "nested.txt"), []byte("nested\n"), 0644)

	var buf bytes.Buffer
	if err := TarCreate(dir, &buf); err != nil {
		t.Fatal(err)
	}

	// Parse the tar and check timestamps
	tr := tar.NewReader(&buf)
	epoch := time.Unix(0, 0)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}

		if !hdr.ModTime.Equal(epoch) {
			t.Errorf("entry %q has ModTime %v; want Unix epoch (1970-01-01 00:00:00 UTC).\n"+
				"  TarCreate must normalize timestamps for deterministic output.",
				hdr.Name, hdr.ModTime)
		}
	}
}

// TestTarCreateOwnerNumeric verifies that TarCreate uses --numeric-owner
// so that user/group names don't leak into the archive (which would vary
// by system and break reproducibility of the tar byte stream across machines).
func TestTarCreateOwnerNumeric(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data\n"), 0644)

	var buf bytes.Buffer
	if err := TarCreate(dir, &buf); err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(&buf)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}

		// With --numeric-owner, Uname and Gname should be empty
		if hdr.Uname != "" || hdr.Gname != "" {
			t.Errorf("entry %q has Uname=%q Gname=%q; --numeric-owner should produce empty names",
				hdr.Name, hdr.Uname, hdr.Gname)
		}
	}
}

// TestTarCreateEntriesSorted verifies that --sort=name produces entries in
// alphabetical order regardless of directory readdir order. Without --sort=name
// the output depends on filesystem ordering, which varies across kernels and
// FS types — a silent source of nondeterminism that the byte-equality test
// might miss if the test directory happens to populate sorted by accident.
func TestTarCreateEntriesSorted(t *testing.T) {
	dir := t.TempDir()
	// Create files in deliberately reverse-alphabetical order so that any
	// readdir() that returns insertion order would yield the wrong sequence.
	for _, name := range []string{"zzz", "yyy", "mmm", "bbb", "aaa"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	if err := TarCreate(dir, &buf); err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(&buf)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		// Skip the leading "./" entry and other directories.
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		names = append(names, hdr.Name)
	}

	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Fatalf("entries not in sorted order — --sort=name appears not to be honored: %v", names)
		}
	}
}

// TestTarCreateContentDeterminismCrossDir verifies that two directories with
// identical contents (but at different paths) produce identical tar byte
// streams. The `-C dir .` invocation should make the absolute path of the
// source directory invisible in the output. If TarCreate ever switched to
// `tar -c dir/` (recording the dir name) this test would fail.
func TestTarCreateContentDeterminismCrossDir(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	for _, dir := range []string{dirA, dirB} {
		os.MkdirAll(filepath.Join(dir, "etc"), 0755)
		if err := os.WriteFile(filepath.Join(dir, "etc", "hostname"), []byte("test\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("world\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	var bufA, bufB bytes.Buffer
	if err := TarCreate(dirA, &bufA); err != nil {
		t.Fatal(err)
	}
	if err := TarCreate(dirB, &bufB); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(bufA.Bytes(), bufB.Bytes()) {
		t.Fatalf("tarballs from %s and %s with identical contents differ — the source path is leaking into the tar headers", dirA, dirB)
	}
}

// TestTarCreateMissingDir verifies TarCreate returns a useful error when its
// source directory doesn't exist, rather than silently producing an empty or
// partial archive.
func TestTarCreateMissingDir(t *testing.T) {
	var buf bytes.Buffer
	err := TarCreate("/nonexistent/path/that/should/not/exist", &buf)
	if err == nil {
		t.Fatal("expected error for nonexistent source dir")
	}
}
