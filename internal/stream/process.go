// Package stream provides byte-stream primitives: SHA-256 readers/writers,
// subprocess-backed decompressors, tar create/extract/list, `.deb` ar-member
// extraction, and GPG verification. The subprocess-backed helpers are pure
// data transforms (bytes in, bytes out, or bytes to a target path) and run
// their tools directly on the host.
package stream

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
)

// procReadCloser spawns an external process, pipes input to its stdin,
// and exposes stdout as an io.ReadCloser. Close() waits for the process
// to complete and returns an error with stderr content on non-zero exit.
type procReadCloser struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr *bytes.Buffer
	done   bool
}

// newProcReadCloser creates a procReadCloser that spawns the given command,
// feeds inputReader to its stdin, and allows reading from its stdout.
func newProcReadCloser(inputReader io.Reader, name string, args ...string) (*procReadCloser, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = inputReader

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stream: stdout pipe for %s: %w", name, err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("stream: start %s: %w", name, err)
	}

	return &procReadCloser{
		cmd:    cmd,
		stdout: stdout,
		stderr: &stderrBuf,
	}, nil
}

// Read reads from the process stdout.
func (p *procReadCloser) Read(buf []byte) (int, error) {
	return p.stdout.Read(buf)
}

// Close closes stdout and waits for the process to exit.
// Returns an error with stderr content if the process exits non-zero.
func (p *procReadCloser) Close() error {
	if p.done {
		return nil
	}
	p.done = true

	// Close stdout pipe — this signals to Wait that we're done reading.
	// Ignore error here; we care about the process exit status.
	_ = p.stdout.Close()

	err := p.cmd.Wait()
	if err != nil {
		stderrContent := p.stderr.String()
		if stderrContent != "" {
			return fmt.Errorf("stream: %s exited with error: %w\nstderr: %s", p.cmd.Path, err, stderrContent)
		}
		return fmt.Errorf("stream: %s exited with error: %w", p.cmd.Path, err)
	}
	return nil
}
