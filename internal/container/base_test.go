package container

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/ipc"
)

// runAndCapture execs req with stdout wired to a pipe and returns the captured
// output, failing the test on a non-zero exit.
func runAndCapture(t *testing.T, env ExecEnv, req *ExecRequest) string {
	t.Helper()
	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	req.FDs = []*os.File{devNull, w, devNull}
	pid, err := env.Exec(req)
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}
	out, _ := io.ReadAll(r)
	r.Close()
	code, err := env.Wait(pid)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit %d, output: %s", code, string(out))
	}
	return string(out)
}

func TestBaseExecTrue(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	pid, err := base.Exec(&ExecRequest{Argv: []string{"/bin/true"}})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	code, err := base.Wait(pid)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
}

func TestBaseExecFalse(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	pid, err := base.Exec(&ExecRequest{Argv: []string{"/bin/false"}})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	code, err := base.Wait(pid)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 1 {
		t.Fatalf("expected 1, got %d", code)
	}
}

func TestBaseExecStdoutCapture(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()
	out := runAndCapture(t, base, &ExecRequest{Argv: []string{"/bin/sh", "-c", "echo hello"}})
	if strings.TrimSpace(out) != "hello" {
		t.Fatalf("expected 'hello', got %q", out)
	}
}

func TestBaseExecStdinPipe(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := base.Exec(&ExecRequest{
		Argv: []string{"/bin/cat"},
		FDs:  []*os.File{stdinR, stdoutW, devNull},
	})
	stdinR.Close()
	stdoutW.Close()
	if err != nil {
		t.Fatalf("exec: %v", err)
	}

	stdinW.Write([]byte("piped data\n"))
	stdinW.Close()

	out, _ := io.ReadAll(stdoutR)
	stdoutR.Close()
	code, _ := base.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if string(out) != "piped data\n" {
		t.Fatalf("expected 'piped data\\n', got %q", out)
	}
}

func TestBaseExecEnvVars(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()
	out := runAndCapture(t, base, &ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo $FOO"},
		Env:  []string{"FOO=bar42"},
	})
	if strings.TrimSpace(out) != "bar42" {
		t.Fatalf("expected 'bar42', got %q", out)
	}
}

func TestBaseExecCwd(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()
	out := runAndCapture(t, base, &ExecRequest{
		Argv: []string{"/bin/sh", "-c", "pwd"},
		Cwd:  "/tmp",
	})
	if strings.TrimSpace(out) != "/tmp" {
		t.Fatalf("expected '/tmp', got %q", out)
	}
}

func TestBaseExecInvalidBinary(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()
	if _, err := base.Exec(&ExecRequest{Argv: []string{"/nonexistent/binary"}}); err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
}

func TestBaseExecResetEnv(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	t.Setenv("GLBX_TEST_LEAK_SENTINEL", "should_not_appear")

	out := runAndCapture(t, base, &ExecRequest{
		Argv:     []string{"/usr/bin/env"},
		Env:      []string{"FOO=bar42"},
		ResetEnv: true,
	})

	gotKeys := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if eq := strings.IndexByte(line, '='); eq >= 0 {
			gotKeys[line[:eq]] = line[eq+1:]
		}
	}

	if got, want := gotKeys["PATH"], ipc.DefaultPATH; got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
	if gotKeys["FOO"] != "bar42" {
		t.Errorf("FOO = %q, want bar42", gotKeys["FOO"])
	}
	if _, leaked := gotKeys["GLBX_TEST_LEAK_SENTINEL"]; leaked {
		t.Errorf("ResetEnv:true leaked host env")
	}
	if len(gotKeys) != 2 {
		t.Errorf("expected exactly 2 env entries (PATH, FOO), got %d: %v", len(gotKeys), gotKeys)
	}
}

func TestBaseExecOverlayInheritsHost(t *testing.T) {
	base := NewBaseExecEnv()
	defer base.Close()

	t.Setenv("GLBX_TEST_HOST_VAR", "host_value")

	out := runAndCapture(t, base, &ExecRequest{Argv: []string{"/usr/bin/env"}})
	if !strings.Contains(out, "GLBX_TEST_HOST_VAR=host_value") {
		t.Errorf("expected host env to flow through; output:\n%s", out)
	}

	out = runAndCapture(t, base, &ExecRequest{
		Argv: []string{"/usr/bin/env"},
		Env:  []string{"FOO=overlay_added"},
	})
	if !strings.Contains(out, "GLBX_TEST_HOST_VAR=host_value") {
		t.Errorf("overlay dropped host env; output:\n%s", out)
	}
	if !strings.Contains(out, "FOO=overlay_added") {
		t.Errorf("overlay key missing; output:\n%s", out)
	}
}

func TestBaseFsContextOpenRead(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/hello.txt"
	if err := os.WriteFile(path, []byte("hello fscontext"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	fs := NewBaseFsContext()
	f, err := fs.Open(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello fscontext" {
		t.Fatalf("got %q, want %q", string(data), "hello fscontext")
	}
}

func TestBaseFsContextOpenWrite(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/out.txt"

	fs := NewBaseFsContext()
	f, err := fs.Open(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := f.Write([]byte("written by fscontext")); err != nil {
		f.Close()
		t.Fatalf("write: %v", err)
	}
	f.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "written by fscontext" {
		t.Fatalf("got %q, want %q", string(data), "written by fscontext")
	}
}

func TestBaseFsContextMkdirSymlinkMkTempDir(t *testing.T) {
	tmp := t.TempDir()
	fs := NewBaseFsContext()

	sub := tmp + "/a/b/c"
	if err := fs.Mkdir(sub, 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if fi, err := os.Stat(sub); err != nil || !fi.IsDir() {
		t.Fatalf("Mkdir did not create directory: %v", err)
	}

	if err := fs.Symlink("c", tmp+"/a/b/link"); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if target, err := os.Readlink(tmp + "/a/b/link"); err != nil || target != "c" {
		t.Fatalf("Readlink = %q, %v", target, err)
	}

	dir, err := fs.MkTempDir(tmp, "scratch-")
	if err != nil {
		t.Fatalf("MkTempDir: %v", err)
	}
	if !strings.HasPrefix(dir, tmp+"/scratch-") {
		t.Fatalf("MkTempDir path = %q", dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("MkTempDir did not create directory: %v", err)
	}
}
