package build

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// execIn runs a command inside the container, routing stdout/stderr through
// the project's log writers. Returns a non-nil error on transport failure or
// non-zero exit.
func execIn(ctx context.Context, cont *container.Container, env []string, creds *container.Credentials, cwd string, argv ...string) error {
	l := log.From(ctx, log.Exec)
	l.Debug("%s", strings.Join(argv, " "))
	stdout, stderr, closeFn := log.NewExecWriters(ctx, log.Exec)
	defer closeFn()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devNull.Close()

	err = container.Run(cont, &container.ExecRequest{
		Argv:        argv,
		Env:         env,
		Cwd:         cwd,
		Credentials: creds,
		FDs:         []*os.File{devNull, stdout, stderr},
	})
	stdout.Close()
	stderr.Close()
	return err
}

// pipeFromContainer reads a file from inside the container and streams it into
// the object store. The container hands back a real file descriptor for the
// path over its IPC channel, so the bytes flow straight into the hash-on-write
// store without buffering the whole file or involving an in-container process.
func pipeFromContainer(cont *container.Container, store *objstore.Store, path string) (objstore.Hash, error) {
	f, err := cont.Open(path, os.O_RDONLY, 0)
	if err != nil {
		return objstore.Hash{}, err
	}
	defer f.Close()

	hash, err := store.Blobs.Store(f)
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("store %s: %w", path, err)
	}
	return hash, nil
}

// execCapture runs a command inside the container and captures its stdout.
func execCapture(ctx context.Context, cont *container.Container, env []string, cwd string, argv ...string) (string, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		r.Close()
		w.Close()
		return "", err
	}
	defer devNull.Close()

	_, stderrFD, closeFn := log.NewExecWriters(ctx, log.Exec)

	pid, err := cont.Exec(&container.ExecRequest{
		Argv: argv,
		Env:  env,
		Cwd:  cwd,
		FDs:  []*os.File{devNull, w, stderrFD},
	})
	w.Close()
	stderrFD.Close()
	if err != nil {
		closeFn()
		r.Close()
		return "", err
	}

	output, _ := io.ReadAll(r)
	r.Close()

	exitCode, err := cont.Wait(pid)
	closeFn()
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		return "", fmt.Errorf("%s exited with code %d", argv[0], exitCode)
	}

	return string(output), nil
}
