package container

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/gardenlinux/glbx/internal/ipc"
)

type BaseExecEnv struct {
	mu    sync.Mutex
	procs map[int]*exec.Cmd
}

func NewBaseExecEnv() *BaseExecEnv {
	return &BaseExecEnv{
		procs: make(map[int]*exec.Cmd),
	}
}

func (b *BaseExecEnv) Exec(req *ExecRequest) (int, error) {
	if len(req.Argv) == 0 {
		return 0, fmt.Errorf("empty argv")
	}

	cmd := exec.Command(req.Argv[0], req.Argv[1:]...)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	cmd.Env = ipc.ResolveEnv(req.ResetEnv, req.Env, os.Environ())

	cmd.SysProcAttr = &syscall.SysProcAttr{}
	if req.UnshareFlags != 0 {
		cmd.SysProcAttr.Cloneflags = req.UnshareFlags
	}
	if req.Credentials != nil {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: req.Credentials.UID,
			Gid: req.Credentials.GID,
		}
	}

	if req.UnshareFlags&syscall.CLONE_NEWUSER != 0 {
		cmd.SysProcAttr.AmbientCaps = allCaps()
	}

	if len(req.FDs) > 0 {
		cmd.Stdin = req.FDs[0]
		if len(req.FDs) > 1 {
			cmd.Stdout = req.FDs[1]
		}
		if len(req.FDs) > 2 {
			cmd.Stderr = req.FDs[2]
		}
		if len(req.FDs) > 3 {
			cmd.ExtraFiles = req.FDs[3:]
		}
	}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start process: %w", err)
	}

	pid := cmd.Process.Pid
	b.mu.Lock()
	b.procs[pid] = cmd
	b.mu.Unlock()

	return pid, nil
}

func (b *BaseExecEnv) Wait(pid int) (int, error) {
	b.mu.Lock()
	cmd, ok := b.procs[pid]
	b.mu.Unlock()

	if !ok {
		return -1, fmt.Errorf("unknown pid %d", pid)
	}

	err := cmd.Wait()

	b.mu.Lock()
	delete(b.procs, pid)
	b.mu.Unlock()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

func (b *BaseExecEnv) Close() error {
	return nil
}

func allCaps() []uintptr {
	caps := make([]uintptr, 0, 41)
	for i := uintptr(0); i <= 40; i++ {
		caps = append(caps, i)
	}
	return caps
}

// BaseFsContext implements FsContext with direct OS and syscall operations on
// the host filesystem — no exec_env_stub is involved.
type BaseFsContext struct{}

func NewBaseFsContext() *BaseFsContext {
	return &BaseFsContext{}
}

func (b *BaseFsContext) Mkdir(path string, mode uint32) error {
	return os.MkdirAll(path, os.FileMode(mode))
}

func (b *BaseFsContext) CreateFile(path string, mode uint32) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, os.FileMode(mode))
	if err != nil {
		return err
	}
	return f.Close()
}

func (b *BaseFsContext) Symlink(target, linkpath string) error {
	os.Remove(linkpath)
	return os.Symlink(target, linkpath)
}

func (b *BaseFsContext) Rmdir(path string) error {
	return os.Remove(path)
}

func (b *BaseFsContext) Unlink(path string) error {
	return os.Remove(path)
}

func (b *BaseFsContext) Mount(source, target, fsType string, flags uintptr, data string) error {
	return syscall.Mount(source, target, fsType, flags, data)
}

func (b *BaseFsContext) Umount(target string, flags int) error {
	return syscall.Unmount(target, flags)
}

func (b *BaseFsContext) Open(path string, flag int, perm uint32) (*os.File, error) {
	return os.OpenFile(path, flag, os.FileMode(perm))
}

func (b *BaseFsContext) MkTempDir(dir, prefix string) (string, error) {
	return doMkTempDir(dir, prefix, func(path string) error {
		return os.Mkdir(path, 0700)
	})
}
