package stream

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildTestDeb builds a minimal .deb file using dpkg-deb -b. Returns the path
// to the resulting .deb. Skips the test if dpkg-deb is not available.
func buildTestDeb(t *testing.T, name string, dataFiles map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("dpkg-deb not available")
	}
	root := t.TempDir()
	pkgDir := filepath.Join(root, name)

	if err := os.MkdirAll(filepath.Join(pkgDir, "DEBIAN"), 0755); err != nil {
		t.Fatal(err)
	}
	control := fmt.Sprintf(`Package: %s
Version: 1.0
Architecture: all
Maintainer: test <t@test>
Description: test package
`, name)
	if err := os.WriteFile(filepath.Join(pkgDir, "DEBIAN", "control"), []byte(control), 0644); err != nil {
		t.Fatal(err)
	}
	for path, content := range dataFiles {
		full := filepath.Join(pkgDir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	debPath := filepath.Join(root, name+".deb")
	cmd := exec.Command("dpkg-deb", "-b", "--root-owner-group", pkgDir, debPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dpkg-deb -b: %v\n%s", err, out)
	}
	return debPath
}

func TestDebExtractDataRoundTrip(t *testing.T) {
	debPath := buildTestDeb(t, "hello", map[string]string{
		"usr/bin/hello":              "echo hello",
		"usr/share/doc/hello/README": "test readme",
	})

	f, err := os.Open(debPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	dataReader, name, err := DebExtractData(f)
	if err != nil {
		t.Fatalf("DebExtractData: %v", err)
	}

	if !strings.HasPrefix(name, "data.tar") {
		t.Fatalf("expected data.tar* member name, got %q", name)
	}

	// Decompress + extract into a temp dir and verify our payload made it through.
	outDir := t.TempDir()
	if err := TarExtractCompressed(dataReader, name, outDir, 0); err != nil {
		t.Fatalf("TarExtractCompressed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(outDir, "usr/bin/hello"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(got) != "echo hello" {
		t.Fatalf("usr/bin/hello content: %q", got)
	}
	got2, err := os.ReadFile(filepath.Join(outDir, "usr/share/doc/hello/README"))
	if err != nil {
		t.Fatalf("read extracted README: %v", err)
	}
	if string(got2) != "test readme" {
		t.Fatalf("README content: %q", got2)
	}
}

func TestDebExtractDataBadMagic(t *testing.T) {
	r := bytes.NewReader([]byte("not an ar archive at all"))
	_, _, err := DebExtractData(r)
	if err == nil {
		t.Fatal("expected error for bad magic")
	}
	if !strings.Contains(err.Error(), "magic") {
		t.Fatalf("error should mention magic: %v", err)
	}
}

func TestDebExtractDataTruncated(t *testing.T) {
	// Magic is fine but the file ends before any member can be read.
	r := bytes.NewReader([]byte("!<arch>\n"))
	_, _, err := DebExtractData(r)
	if err == nil {
		t.Fatal("expected error on truncated archive")
	}
}

func TestDebExtractDataMissingDataMember(t *testing.T) {
	// Build an ar archive that only contains debian-binary, no data.tar — so
	// DebExtractData should walk past it, hit EOF, and return an error.
	var buf bytes.Buffer
	buf.WriteString("!<arch>\n")
	writeArMember(&buf, "debian-binary", []byte("2.0\n"))

	_, _, err := DebExtractData(&buf)
	if err == nil {
		t.Fatal("expected error when no data.tar member present")
	}
}

func TestDebExtractDataSkipsControlMember(t *testing.T) {
	// debian-binary, then control.tar.gz, then data.tar.gz: verify we skip the
	// first two members (one with odd size to exercise padding) and return the
	// third.
	var buf bytes.Buffer
	buf.WriteString("!<arch>\n")
	// 5 bytes — odd, exercises the padding-byte skip path.
	writeArMember(&buf, "debian-binary", []byte("2.0\n\n"))
	writeArMember(&buf, "control.tar.gz", []byte("control-bytes"))
	writeArMember(&buf, "data.tar.xz", []byte("data-bytes"))

	dataReader, name, err := DebExtractData(&buf)
	if err != nil {
		t.Fatalf("DebExtractData: %v", err)
	}
	if name != "data.tar.xz" {
		t.Fatalf("expected data.tar.xz, got %q", name)
	}
	got, err := io.ReadAll(dataReader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data-bytes" {
		t.Fatalf("data member content: %q", got)
	}
}

func TestReadArHeaderInvalidMagic(t *testing.T) {
	// 60 bytes ending in something other than "`\n" — invalid header magic.
	hdr := make([]byte, 60)
	for i := range hdr {
		hdr[i] = ' '
	}
	copy(hdr, "name            ")
	copy(hdr[48:58], "10        ")
	hdr[58] = 'X' // wrong magic byte
	hdr[59] = 'Y'

	_, _, err := readArHeader(bytes.NewReader(hdr))
	if err == nil {
		t.Fatal("expected error for bad header magic")
	}
}

func TestReadArHeaderBadSize(t *testing.T) {
	hdr := make([]byte, 60)
	for i := range hdr {
		hdr[i] = ' '
	}
	copy(hdr, "name/           ")
	copy(hdr[48:58], "notanumber")
	hdr[58] = '`'
	hdr[59] = '\n'

	_, _, err := readArHeader(bytes.NewReader(hdr))
	if err == nil {
		t.Fatal("expected error for non-numeric size")
	}
}

func TestReadArHeaderTrimsTrailingSlash(t *testing.T) {
	// BSD/GNU ar appends "/" to a member name as a terminator. We strip it.
	hdr := make([]byte, 60)
	for i := range hdr {
		hdr[i] = ' '
	}
	copy(hdr, "data.tar.xz/    ")
	copy(hdr[48:58], "0         ")
	hdr[58] = '`'
	hdr[59] = '\n'

	name, size, err := readArHeader(bytes.NewReader(hdr))
	if err != nil {
		t.Fatal(err)
	}
	if name != "data.tar.xz" {
		t.Fatalf("name: %q", name)
	}
	if size != 0 {
		t.Fatalf("size: %d", size)
	}
}

// writeArMember appends a single ar member (60-byte header + data + optional
// padding byte) to buf.
func writeArMember(buf *bytes.Buffer, name string, data []byte) {
	hdr := make([]byte, 60)
	for i := range hdr {
		hdr[i] = ' '
	}
	// Member name padded to 16 bytes (no trailing slash — the parser tolerates
	// either form, but we exercise the no-slash path here).
	copy(hdr, name)
	// Size at offset 48..58.
	copy(hdr[48:58], fmt.Sprintf("%-10d", len(data)))
	// End magic.
	hdr[58] = '`'
	hdr[59] = '\n'
	buf.Write(hdr)
	buf.Write(data)
	if len(data)%2 == 1 {
		buf.WriteByte('\n')
	}
}
