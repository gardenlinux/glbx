package stream

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
)

// TarExtract extracts a tar stream (already decompressed) into the target directory.
// Spawns `tar -C <target> -x` with optional --strip-components=N.
//
// This is a terminal sink — it reads from the io.Reader until EOF and blocks
// until extraction is complete.
func TarExtract(r io.Reader, targetDir string, stripComponents int) error {
	args := []string{"-C", targetDir, "-x"}
	if stripComponents > 0 {
		args = append(args, "--strip-components="+strconv.Itoa(stripComponents))
	}

	cmd := exec.Command("tar", args...)
	cmd.Stdin = r

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Run(); err != nil {
		stderrContent := stderrBuf.String()
		if stderrContent != "" {
			return fmt.Errorf("stream: tar extract to %s failed: %w\nstderr: %s", targetDir, err, stderrContent)
		}
		return fmt.Errorf("stream: tar extract to %s failed: %w", targetDir, err)
	}
	return nil
}

// TarExtractCompressed decompresses and extracts a compressed tar archive.
// It automatically selects the decompressor based on the filename extension,
// then extracts to the target directory with optional strip-components.
func TarExtractCompressed(r io.Reader, filename string, targetDir string, stripComponents int) error {
	decomp, err := AutoDecompress(r, filename)
	if err != nil {
		return fmt.Errorf("stream: tar extract compressed %s: %w", filename, err)
	}
	defer decomp.Close()

	return TarExtract(decomp, targetDir, stripComponents)
}

// TarCreate creates a tar archive of the given directory, writing it to w.
// Uses tar -c -C <dir> . to capture the full directory contents.
func TarCreate(dir string, w io.Writer) error {
	cmd := exec.Command("tar", "--mtime=@0", "--numeric-owner", "--sort=name", "-c", "-C", dir, ".")
	cmd.Stdout = w

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Run(); err != nil {
		stderrContent := stderrBuf.String()
		if stderrContent != "" {
			return fmt.Errorf("stream: tar create from %s failed: %w\nstderr: %s", dir, err, stderrContent)
		}
		return fmt.Errorf("stream: tar create from %s failed: %w", dir, err)
	}
	return nil
}

// TarList lists the entries in a tar stream without extracting.
func TarList(r io.Reader) ([]string, error) {
	cmd := exec.Command("tar", "-t")
	cmd.Stdin = r

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("stream: tar list failed: %w", err)
	}

	var entries []string
	for _, line := range bytes.Split(stdoutBuf.Bytes(), []byte("\n")) {
		if len(line) > 0 {
			entries = append(entries, string(line))
		}
	}
	return entries, nil
}

// TarExtractFile extracts a single file from a tar stream, returning its contents.
// Uses `tar -xO <path>` to pipe the file's content to stdout.
func TarExtractFile(r io.Reader, path string) ([]byte, error) {
	cmd := exec.Command("tar", "-xO", path)
	cmd.Stdin = r

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	if err := cmd.Run(); err != nil {
		stderrContent := stderrBuf.String()
		if stderrContent != "" {
			return nil, fmt.Errorf("stream: tar extract file %s: %w\nstderr: %s", path, err, stderrContent)
		}
		return nil, fmt.Errorf("stream: tar extract file %s: %w", path, err)
	}
	return stdoutBuf.Bytes(), nil
}
