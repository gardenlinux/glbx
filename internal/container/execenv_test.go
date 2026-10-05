package container

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/ipc"
	"github.com/gardenlinux/glbx/internal/log"
)

func testCtx() context.Context {
	return log.WithTarget(context.Background(), log.Discard)
}

// requireStub returns the prebuilt stub path, skipping the test when it is
// unset or missing, or when unprivileged user namespaces are unavailable.
func requireStub(t *testing.T) string {
	t.Helper()
	stubPath := os.Getenv("GLBX_EXEC_ENV_STUB")
	if stubPath == "" {
		t.Skip("GLBX_EXEC_ENV_STUB not set; run `make test` (the Makefile sets it for you)")
	}
	if _, err := os.Stat(stubPath); err != nil {
		t.Skipf("GLBX_EXEC_ENV_STUB=%q does not exist; run `make build` first", stubPath)
	}
	if !userNSAvailable() {
		t.Skip("unprivileged user namespaces unavailable on this host")
	}
	return stubPath
}

// userNSAvailable probes whether an unprivileged user namespace with inner
// root can be created, by running `unshare` if present or checking the sysctl.
func userNSAvailable() bool {
	if data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(data)) == "0" {
			return false
		}
	}
	cmd := exec.Command("unshare", "--user", "--map-root-user", "true")
	return cmd.Run() == nil
}

func TestUserNSExecTrue(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	pid, err := userNS.Exec(&ExecRequest{
		Argv: []string{"/bin/true"},
		Env:  []string{"PATH=/bin"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	code, err := userNS.Wait(pid)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
}

func TestUserNSExecStdoutCapture(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := userNS.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo userns_hello"},
		Env:  []string{"PATH=/bin:/usr/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := userNS.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(string(out)) != "userns_hello" {
		t.Fatalf("expected 'userns_hello', got %q", string(out))
	}
}

func TestUserNSUIDIsRoot(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := userNS.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "cat /proc/self/status"},
		Env:  []string{"PATH=/bin:/usr/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := userNS.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d, output: %s", code, string(out))
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[1] != "0" {
				t.Fatalf("expected uid 0, got: %s", line)
			}
			return
		}
	}
	t.Fatalf("Uid line not found in output:\n%s", string(out))
}

func TestUserNSCredentialSwitchUID1000(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := userNS.Exec(&ExecRequest{
		Argv:        []string{"/bin/sh", "-c", "cat /proc/self/status"},
		Env:         []string{"PATH=/bin:/usr/bin"},
		Credentials: &Credentials{UID: 1000, GID: 1000},
		FDs:         []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := userNS.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d, output: %s", code, string(out))
	}

	var foundUID, foundGID bool
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "1000" {
				foundUID = true
			} else {
				t.Fatalf("expected uid 1000, got: %s", line)
			}
		}
		if strings.HasPrefix(line, "Gid:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "1000" {
				foundGID = true
			} else {
				t.Fatalf("expected gid 1000, got: %s", line)
			}
		}
	}
	if !foundUID {
		t.Fatalf("Uid line not found")
	}
	if !foundGID {
		t.Fatalf("Gid line not found")
	}
}

func TestUserNSMultipleExecs(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	for i := 0; i < 5; i++ {
		pid, err := userNS.Exec(&ExecRequest{
			Argv: []string{"/bin/true"},
			Env:  []string{"PATH=/bin"},
		})
		if err != nil {
			t.Fatalf("exec %d: %v", i, err)
		}
		code, err := userNS.Wait(pid)
		if err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
		if code != 0 {
			t.Fatalf("exec %d: expected 0, got %d", i, code)
		}
	}
}

// =============================================================================
// Layer 3: MountNS — mount namespace on top of UserNS, can mount tmpfs
// =============================================================================

func TestMountNSExecTrue(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	mountNS, err := NewMountNS(MountNSConfig{Parent: userNS, StubPath: stubPath})
	if err != nil {
		t.Fatalf("create mountns: %v", err)
	}
	defer mountNS.Close()

	pid, err := mountNS.Exec(&ExecRequest{
		Argv: []string{"/bin/true"},
		Env:  []string{"PATH=/bin"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	code, err := mountNS.Wait(pid)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
}

func TestMountNSMountTmpfs(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	mountNS, err := NewMountNS(MountNSConfig{Parent: userNS, StubPath: stubPath})
	if err != nil {
		t.Fatalf("create mountns: %v", err)
	}
	defer mountNS.Close()

	tmpDir := t.TempDir()
	if err := mountNS.Mount("tmpfs", tmpDir, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}

	// Write a file to the tmpfs via a process in the mount ns
	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := mountNS.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo mounted > " + tmpDir + "/test && cat " + tmpDir + "/test"},
		Env:  []string{"PATH=/bin:/usr/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := mountNS.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(string(out)) != "mounted" {
		t.Fatalf("expected 'mounted', got %q", string(out))
	}
}

func TestMountNSMkdir(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	mountNS, err := NewMountNS(MountNSConfig{Parent: userNS, StubPath: stubPath})
	if err != nil {
		t.Fatalf("create mountns: %v", err)
	}
	defer mountNS.Close()

	tmpDir := t.TempDir()
	if err := mountNS.Mount("tmpfs", tmpDir, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}

	newDir := tmpDir + "/subdir"
	if err := mountNS.Mkdir(newDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Verify the dir exists by listing it from inside the ns
	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := mountNS.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "test -d " + newDir + " && echo ok"},
		Env:  []string{"PATH=/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := mountNS.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("expected 'ok', got %q", string(out))
	}
}

func TestMountNSMkTempDir(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	mountNS, err := NewMountNS(MountNSConfig{Parent: userNS, StubPath: stubPath})
	if err != nil {
		t.Fatalf("create mountns: %v", err)
	}
	defer mountNS.Close()

	tmpDir := t.TempDir()
	if err := mountNS.Mount("tmpfs", tmpDir, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}

	dir1, err := mountNS.MkTempDir(tmpDir, "work-")
	if err != nil {
		t.Fatalf("MkTempDir: %v", err)
	}
	dir2, err := mountNS.MkTempDir(tmpDir, "work-")
	if err != nil {
		t.Fatalf("MkTempDir second: %v", err)
	}

	if dir1 == dir2 {
		t.Fatalf("expected distinct paths, got %q twice", dir1)
	}
	if filepath.Dir(dir1) != tmpDir || filepath.Dir(dir2) != tmpDir {
		t.Fatalf("unexpected parent: %q, %q", dir1, dir2)
	}

	// Verify both directories exist inside the namespace.
	for _, d := range []string{dir1, dir2} {
		r, w, _ := os.Pipe()
		devNull, _ := os.Open("/dev/null")
		defer devNull.Close()
		pid, err := mountNS.Exec(&ExecRequest{
			Argv: []string{"/bin/sh", "-c", "test -d " + d + " && echo ok"},
			Env:  []string{"PATH=/bin"},
			FDs:  []*os.File{devNull, w, devNull},
		})
		w.Close()
		if err != nil {
			r.Close()
			t.Fatalf("exec for %q: %v", d, err)
		}
		out, _ := io.ReadAll(r)
		r.Close()
		if code, _ := mountNS.Wait(pid); code != 0 {
			t.Fatalf("%q: exit %d", d, code)
		}
		if strings.TrimSpace(string(out)) != "ok" {
			t.Fatalf("%q: expected 'ok', got %q", d, string(out))
		}
	}
}

// buildMinimalRootfs creates a rootfs with /bin/sh, /bin/cat, /bin/true and
// their library dependencies (libc + dynamic linker). The set of libraries to
// copy and their target paths are discovered with ldd, so this works on any
// host architecture (amd64, arm64, …) without hard-coding paths.
func buildMinimalRootfs(t *testing.T) string {
	t.Helper()
	rootfs := t.TempDir()

	for _, d := range []string{"bin", "proc", "sys", "dev", "tmp", "run", "usr/bin"} {
		os.MkdirAll(rootfs+"/"+d, 0755)
	}

	binaries := map[string]string{
		"/bin/sh":   "bin/sh",
		"/bin/cat":  "bin/cat",
		"/bin/true": "bin/true",
	}
	libsToCopy := map[string]string{}
	for src := range binaries {
		for _, lib := range lddLibs(t, src) {
			// Strip leading slash to make rootfs-relative.
			libsToCopy[lib] = strings.TrimPrefix(lib, "/")
		}
	}
	// Make sure all parent directories under rootfs exist.
	for _, dst := range libsToCopy {
		os.MkdirAll(rootfs+"/"+filepath.Dir(dst), 0755)
	}

	for src, dst := range binaries {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		if err := os.WriteFile(rootfs+"/"+dst, data, 0755); err != nil {
			t.Fatalf("write %s: %v", dst, err)
		}
	}

	for src, dst := range libsToCopy {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		if err := os.WriteFile(rootfs+"/"+dst, data, 0755); err != nil {
			t.Fatalf("write %s: %v", dst, err)
		}
	}

	return rootfs
}

// lddLibs returns the absolute paths of every shared library that the dynamic
// linker would resolve for `bin`, including the linker itself. Pseudo-entries
// like linux-vdso (no on-disk path) are filtered out.
func lddLibs(t *testing.T, bin string) []string {
	t.Helper()
	out, err := exec.Command("ldd", bin).Output()
	if err != nil {
		t.Fatalf("ldd %s: %v", bin, err)
	}
	var libs []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Forms we care about:
		//   "libc.so.6 => /lib/aarch64-linux-gnu/libc.so.6 (0x...)"
		//   "/lib/ld-linux-aarch64.so.1 (0x...)"
		// Form we skip: "linux-vdso.so.1 (0x...)"
		if i := strings.Index(line, " => "); i >= 0 {
			rest := strings.TrimSpace(line[i+4:])
			if strings.HasPrefix(rest, "/") {
				if j := strings.Index(rest, " "); j > 0 {
					libs = append(libs, rest[:j])
				}
			}
		} else if strings.HasPrefix(line, "/") {
			if j := strings.Index(line, " "); j > 0 {
				libs = append(libs, line[:j])
			}
		}
	}
	return libs
}

func TestContainerExecTrue(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	pid, err := cont.Exec(&ExecRequest{
		Argv: []string{"/bin/true"},
		Env:  []string{"PATH=/bin"},
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	code, err := cont.Wait(pid)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
}

func TestContainerExecStdoutCapture(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := cont.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo container_works"},
		Env:  []string{"PATH=/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := cont.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d, output: %s", code, string(out))
	}
	if strings.TrimSpace(string(out)) != "container_works" {
		t.Fatalf("expected 'container_works', got %q", string(out))
	}
}

func TestContainerProcMounted(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := cont.Exec(&ExecRequest{
		Argv: []string{"/bin/cat", "/proc/self/status"},
		Env:  []string{"PATH=/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := cont.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}

	if !strings.Contains(string(out), "Uid:") {
		t.Fatalf("/proc/self/status should contain Uid, got:\n%s", string(out))
	}
}

func TestContainerPIDNamespace(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	// Inside a PID namespace, the first child of the stub gets a low PID
	pid, err := cont.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "cat /proc/self/status"},
		Env:  []string{"PATH=/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := cont.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}

	// In a PID namespace, the first process should have a low PID (the stub is PID 1,
	// its child /bin/sh is PID 2 or thereabouts)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "NSpid:") {
			fields := strings.Fields(line)
			// NSpid shows PID in each namespace; the last field is the innermost
			innerPID := fields[len(fields)-1]
			// Should be a small number (typically 2-4)
			if innerPID == "0" || len(innerPID) > 3 {
				t.Fatalf("expected small inner PID, got NSpid line: %s", line)
			}
			return
		}
	}
	t.Log("NSpid not found — PID ns may not expose it in this kernel config, skipping assertion")
}

func TestContainerCredentials(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	pid, err := cont.Exec(&ExecRequest{
		Argv:        []string{"/bin/cat", "/proc/self/status"},
		Env:         []string{"PATH=/bin"},
		Credentials: &Credentials{UID: 1000, GID: 1000},
		FDs:         []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := cont.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d, output: %s", code, string(out))
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[1] != "1000" {
				t.Fatalf("expected uid 1000, got: %s", line)
			}
		}
		if strings.HasPrefix(line, "Gid:") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[1] != "1000" {
				t.Fatalf("expected gid 1000, got: %s", line)
			}
		}
	}
}

func TestContainerIsolatedFilesystem(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	r, w, _ := os.Pipe()
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()

	// /etc should not exist in our minimal rootfs — verifies pivot_root worked
	pid, err := cont.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "test -d /etc && echo has_etc || echo no_etc"},
		Env:  []string{"PATH=/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	if err != nil {
		r.Close()
		t.Fatalf("exec: %v", err)
	}

	out, _ := io.ReadAll(r)
	r.Close()
	code, _ := cont.Wait(pid)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(string(out)) != "no_etc" {
		t.Fatalf("expected filesystem isolation (no /etc), got %q", string(out))
	}
}

func TestContainerMultipleExecs(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	for i := 0; i < 5; i++ {
		pid, err := cont.Exec(&ExecRequest{
			Argv: []string{"/bin/true"},
			Env:  []string{"PATH=/bin"},
		})
		if err != nil {
			t.Fatalf("exec %d: %v", i, err)
		}
		code, err := cont.Wait(pid)
		if err != nil {
			t.Fatalf("wait %d: %v", i, err)
		}
		if code != 0 {
			t.Fatalf("exec %d: exit %d", i, code)
		}
	}
}

// =============================================================================
// Env propagation: overlay semantics + ResetEnv
// =============================================================================

// TestContainerInheritsStubResetEnv verifies that a child Exec'd inside a
// Container with no Env override sees PATH=ipc.DefaultPATH and nothing else from
// the host. The Container's stub was launched with ResetEnv:true (set in
// NewContainer), so its os.Environ() — which the child inherits — is the
// minimal PATH-only env.
func TestContainerInheritsStubResetEnv(t *testing.T) {
	stubPath := requireStub(t)
	rootfs := buildMinimalRootfs(t)

	t.Setenv("GLBX_TEST_LEAK_SENTINEL", "should_not_reach_container")

	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	// /usr/bin/env isn't in the minimal rootfs, but /bin/sh + builtin set is.
	cont, err := NewContainer(ContainerConfig{Ctx: testCtx(), Parent: userNS, StubPath: stubPath, Rootfs: rootfs})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	defer cont.Close()

	out := runAndCapture(t, cont, &ExecRequest{
		Argv: []string{"/bin/sh", "-c", "set"},
	})

	// PATH must be the reset default.
	if !strings.Contains(out, "PATH='"+ipc.DefaultPATH+"'") && !strings.Contains(out, "PATH="+ipc.DefaultPATH) {
		t.Errorf("expected PATH=%s in container child env; output:\n%s", ipc.DefaultPATH, out)
	}
	// Host sentinel must not be present.
	if strings.Contains(out, "GLBX_TEST_LEAK_SENTINEL") {
		t.Errorf("ResetEnv:true on container stub leaked host env into child; output:\n%s", out)
	}
}

// =============================================================================
// FsContext: BaseFsContext (no stub) and RemoteExecEnv.Open (via MountNS)
// =============================================================================

func TestMountNSOpen(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	mountNS, err := NewMountNS(MountNSConfig{Parent: userNS, StubPath: stubPath})
	if err != nil {
		t.Fatalf("create mountns: %v", err)
	}
	defer mountNS.Close()

	tmpDir := t.TempDir()
	if err := mountNS.Mount("tmpfs", tmpDir, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}

	// Write content to a file via Exec+sh so it goes through the namespace.
	filePath := tmpDir + "/data.txt"
	if err := mountNS.CreateFile(filePath, 0644); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()
	r, w, _ := os.Pipe()
	pid, err := mountNS.Exec(&ExecRequest{
		Argv: []string{"/bin/sh", "-c", "echo -n 'namespace file content' > " + filePath},
		Env:  []string{"PATH=/bin:/usr/bin"},
		FDs:  []*os.File{devNull, w, devNull},
	})
	w.Close()
	r.Close()
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if code, err := mountNS.Wait(pid); err != nil || code != 0 {
		t.Fatalf("write via exec: code=%d err=%v", code, err)
	}

	// Open the file via MountNS.Open — gets FD back via SCM_RIGHTS from stub.
	f, err := mountNS.Open(filePath, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("MountNS.Open: %v", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "namespace file content" {
		t.Fatalf("got %q, want %q", string(data), "namespace file content")
	}
}

func TestMountNSOpenWrite(t *testing.T) {
	stubPath := requireStub(t)
	base := NewBaseExecEnv()
	defer base.Close()

	userNS, err := NewUserNS(UserNSConfig{Ctx: testCtx(), Parent: base, StubPath: stubPath, IDCount: 65536})
	if err != nil {
		t.Fatalf("create userns: %v", err)
	}
	defer userNS.Close()

	mountNS, err := NewMountNS(MountNSConfig{Parent: userNS, StubPath: stubPath})
	if err != nil {
		t.Fatalf("create mountns: %v", err)
	}
	defer mountNS.Close()

	tmpDir := t.TempDir()
	if err := mountNS.Mount("tmpfs", tmpDir, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatalf("mount tmpfs: %v", err)
	}

	filePath := tmpDir + "/write.txt"

	// Open for writing via MountNS — stub opens the file in the namespace.
	f, err := mountNS.Open(filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		t.Fatalf("MountNS.Open for write: %v", err)
	}
	if _, err := f.Write([]byte("written via open fd")); err != nil {
		f.Close()
		t.Fatalf("write to fd: %v", err)
	}
	f.Close()

	// Verify the content by reading via Exec+cat inside the namespace.
	devNull, _ := os.Open("/dev/null")
	defer devNull.Close()
	rr, ww, _ := os.Pipe()
	pid, err := mountNS.Exec(&ExecRequest{
		Argv: []string{"/bin/cat", filePath},
		Env:  []string{"PATH=/bin:/usr/bin"},
		FDs:  []*os.File{devNull, ww, devNull},
	})
	ww.Close()
	if err != nil {
		rr.Close()
		t.Fatalf("exec cat: %v", err)
	}
	out, _ := io.ReadAll(rr)
	rr.Close()
	if code, err := mountNS.Wait(pid); err != nil || code != 0 {
		t.Fatalf("cat: code=%d err=%v", code, err)
	}
	if string(out) != "written via open fd" {
		t.Fatalf("cat got %q, want %q", string(out), "written via open fd")
	}
}
