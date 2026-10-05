package build

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const testControl = `Source: foo
Section: utils

Package: foo
Architecture: any
Provides: foo-virt (= 1.0), bar-impl

Package: foo-utils
Architecture: any

Package: libfoo1
Architecture: any
Provides: libfoo
`

func writeControl(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "src", "debian"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "debian", "control"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestParseBinaryPackageNames(t *testing.T) {
	dir := t.TempDir()
	writeControl(t, dir, testControl)
	got := ParseBinaryPackageNames(dir)
	want := []string{"foo", "foo-utils", "libfoo1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseBinaryPackageNames: got %v, want %v", got, want)
	}
}

func TestParseBinaryPackageProvides(t *testing.T) {
	dir := t.TempDir()
	writeControl(t, dir, testControl)
	got := ParseBinaryPackageProvides(dir)
	want := []string{"foo-virt", "bar-impl", "libfoo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseBinaryPackageProvides: got %v, want %v", got, want)
	}
}

func TestParseBinaryPackageProvidesMap(t *testing.T) {
	dir := t.TempDir()
	writeControl(t, dir, testControl)
	got := ParseBinaryPackageProvidesMap(dir)
	want := map[string][]string{
		"foo":     {"foo-virt", "bar-impl"},
		"libfoo1": {"libfoo"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseBinaryPackageProvidesMap: got %v, want %v", got, want)
	}
}

func TestParseBinaryPackageNames_Missing(t *testing.T) {
	dir := t.TempDir()
	if got := ParseBinaryPackageNames(dir); got != nil {
		t.Fatalf("expected nil for missing control, got %v", got)
	}
}
