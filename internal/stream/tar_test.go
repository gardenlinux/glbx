package stream

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTarExtract_Basic(t *testing.T) {
	// Create a temporary directory with some files
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "hello.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(srcDir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "subdir", "world.txt"), []byte("world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a tar archive
	var tarBuf bytes.Buffer
	cmd := exec.Command("tar", "-C", srcDir, "-c", ".")
	cmd.Stdout = &tarBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("creating tar: %v", err)
	}

	// Extract to a new directory
	dstDir := t.TempDir()
	if err := TarExtract(&tarBuf, dstDir, 0); err != nil {
		t.Fatalf("TarExtract: %v", err)
	}

	// Verify files
	data, err := os.ReadFile(filepath.Join(dstDir, "hello.txt"))
	if err != nil {
		t.Fatalf("reading hello.txt: %v", err)
	}
	if string(data) != "hello\n" {
		t.Errorf("hello.txt = %q, want %q", string(data), "hello\n")
	}

	data, err = os.ReadFile(filepath.Join(dstDir, "subdir", "world.txt"))
	if err != nil {
		t.Fatalf("reading subdir/world.txt: %v", err)
	}
	if string(data) != "world\n" {
		t.Errorf("subdir/world.txt = %q, want %q", string(data), "world\n")
	}
}

func TestTarExtract_StripComponents(t *testing.T) {
	// Create a tar with a top-level directory prefix
	srcDir := t.TempDir()
	topDir := filepath.Join(srcDir, "pkg-1.0")
	if err := os.Mkdir(topDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(topDir, "file.txt"), []byte("content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var tarBuf bytes.Buffer
	cmd := exec.Command("tar", "-C", srcDir, "-c", "pkg-1.0")
	cmd.Stdout = &tarBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("creating tar: %v", err)
	}

	// Extract with strip-components=1
	dstDir := t.TempDir()
	if err := TarExtract(&tarBuf, dstDir, 1); err != nil {
		t.Fatalf("TarExtract with strip: %v", err)
	}

	// File should be directly in dstDir, not under pkg-1.0/
	data, err := os.ReadFile(filepath.Join(dstDir, "file.txt"))
	if err != nil {
		t.Fatalf("reading file.txt: %v", err)
	}
	if string(data) != "content\n" {
		t.Errorf("file.txt = %q, want %q", string(data), "content\n")
	}

	// pkg-1.0 directory should NOT exist
	if _, err := os.Stat(filepath.Join(dstDir, "pkg-1.0")); !os.IsNotExist(err) {
		t.Error("pkg-1.0 directory should not exist after strip-components=1")
	}
}

func TestTarExtract_InvalidTar(t *testing.T) {
	dstDir := t.TempDir()
	err := TarExtract(bytes.NewReader([]byte("not a tar")), dstDir, 0)
	if err == nil {
		t.Fatal("expected error for invalid tar data, got nil")
	}
}

func TestTarExtract_EmptyTar(t *testing.T) {
	// Create an empty tar (just the end-of-archive markers)
	var tarBuf bytes.Buffer
	emptyDir := t.TempDir()
	cmd := exec.Command("tar", "-C", emptyDir, "-c", "--files-from=/dev/null")
	cmd.Stdout = &tarBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("creating empty tar: %v", err)
	}

	dstDir := t.TempDir()
	if err := TarExtract(&tarBuf, dstDir, 0); err != nil {
		t.Fatalf("TarExtract empty: %v", err)
	}
}

func TestTarExtract_Permissions(t *testing.T) {
	// Create files with specific permissions
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "exec.sh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "readonly.txt"), []byte("ro\n"), 0444); err != nil {
		t.Fatal(err)
	}

	var tarBuf bytes.Buffer
	cmd := exec.Command("tar", "-C", srcDir, "-c", ".")
	cmd.Stdout = &tarBuf
	if err := cmd.Run(); err != nil {
		t.Fatalf("creating tar: %v", err)
	}

	dstDir := t.TempDir()
	if err := TarExtract(&tarBuf, dstDir, 0); err != nil {
		t.Fatalf("TarExtract: %v", err)
	}

	// Check executable permission
	info, err := os.Stat(filepath.Join(dstDir, "exec.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("exec.sh should be executable")
	}
}

func TestTarExtractCompressed(t *testing.T) {
	// Create a source directory
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "data.txt"), []byte("compressed tar test\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		filename string
		compTool string
		compArgs []string
	}{
		{"xz", "archive.tar.xz", "xz", []string{"-c"}},
		{"gz", "archive.tar.gz", "gzip", []string{"-c"}},
		{"bz2", "archive.tar.bz2", "bzip2", []string{"-c"}},
		{"zst", "archive.tar.zst", "zstd", []string{"-c"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create uncompressed tar
			var tarBuf bytes.Buffer
			cmd := exec.Command("tar", "-C", srcDir, "-c", ".")
			cmd.Stdout = &tarBuf
			if err := cmd.Run(); err != nil {
				t.Fatalf("creating tar: %v", err)
			}

			// Compress it
			compCmd := exec.Command(tt.compTool, tt.compArgs...)
			compCmd.Stdin = &tarBuf
			var compBuf bytes.Buffer
			compCmd.Stdout = &compBuf
			if err := compCmd.Run(); err != nil {
				t.Fatalf("compressing with %s: %v", tt.compTool, err)
			}

			// Extract with TarExtractCompressed
			dstDir := t.TempDir()
			if err := TarExtractCompressed(&compBuf, tt.filename, dstDir, 0); err != nil {
				t.Fatalf("TarExtractCompressed: %v", err)
			}

			// Verify
			data, err := os.ReadFile(filepath.Join(dstDir, "data.txt"))
			if err != nil {
				t.Fatalf("reading data.txt: %v", err)
			}
			if string(data) != "compressed tar test\n" {
				t.Errorf("data.txt = %q, want %q", string(data), "compressed tar test\n")
			}
		})
	}
}

func TestTarExtractCompressed_UnsupportedExtension(t *testing.T) {
	err := TarExtractCompressed(bytes.NewReader(nil), "file.tar.lz4", t.TempDir(), 0)
	if err == nil {
		t.Fatal("expected error for unsupported extension")
	}
}
