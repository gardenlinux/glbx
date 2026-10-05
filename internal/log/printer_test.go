package log

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLogPrinterSmoke runs a LogPrinter against a real BufferTarget and
// confirms records emitted both before and while running are drained, Stop
// unblocks and is idempotent, and Info lands on stdout while the other levels
// land on stderr.
func TestLogPrinterSmoke(t *testing.T) {
	stdout, stderr, restore := redirectStdio(t)
	defer restore()

	bt := NewBufferTarget()
	bt.Emit(Record{Level: Info, Component: Build, Msg: "before-run"})

	p := NewLogPrinter(bt)
	p.colorErr = false // keep output monochrome regardless of TTY
	p.Run()

	bt.Emit(Record{Level: Warn, Component: Importer, Msg: "after-run-warn"})
	bt.Emit(Record{Level: Error, Component: Engine, Msg: "after-run-error"})
	bt.Emit(Record{Level: Debug, Component: Container, Msg: "after-run-debug"})

	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("LogPrinter.Stop did not return within 2s — printer goroutine likely deadlocked")
	}

	p.Stop() // idempotent

	out := stdout.String()
	err := stderr.String()
	combined := out + err

	for _, want := range []string{"before-run", "after-run-warn", "after-run-error", "after-run-debug"} {
		if !strings.Contains(combined, want) {
			t.Errorf("printer output missing %q\nstdout:\n%s\nstderr:\n%s", want, out, err)
		}
	}
	if !strings.Contains(out, "before-run") {
		t.Errorf("Info record should be on stdout, got stdout=%q", out)
	}
	if !strings.Contains(err, "after-run-warn") {
		t.Errorf("Warn record should be on stderr, got stderr=%q", err)
	}
}

// redirectStdio swaps os.Stdout and os.Stderr for pipes, returning buffers that
// receive the captured bytes and a restore func. Drain goroutines keep the
// writers from blocking when a pipe buffer fills.
func redirectStdio(t *testing.T) (stdout, stderr *bytes.Buffer, restore func()) {
	t.Helper()
	origStdout := os.Stdout
	origStderr := os.Stderr

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	os.Stdout = outW
	os.Stderr = errW

	stdout = &bytes.Buffer{}
	stderr = &bytes.Buffer{}

	doneOut := make(chan struct{})
	doneErr := make(chan struct{})
	go func() {
		_, _ = io.Copy(stdout, outR)
		close(doneOut)
	}()
	go func() {
		_, _ = io.Copy(stderr, errR)
		close(doneErr)
	}()

	restore = func() {
		outW.Close()
		errW.Close()
		<-doneOut
		<-doneErr
		os.Stdout = origStdout
		os.Stderr = origStderr
	}
	return stdout, stderr, restore
}
