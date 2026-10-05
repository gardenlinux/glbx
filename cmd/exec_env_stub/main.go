package main

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/gardenlinux/glbx/internal/ipc"
)

func main() {
	sockFile := os.NewFile(3, "ipc-socket")
	if sockFile == nil {
		fmt.Fprintf(os.Stderr, "exec_env_stub: fd 3 not available\n")
		os.Exit(1)
	}
	server := ipc.NewServer(sockFile)

	stub := &stubServer{
		procs: make(map[int]*exec.Cmd),
	}

	server.Register(ipc.FuncExec, stub.handleExec)
	server.Register(ipc.FuncWait, stub.handleWait)
	server.Register(ipc.FuncMount, stub.handleMount)
	server.Register(ipc.FuncMkdir, stub.handleMkdir)
	server.Register(ipc.FuncCreateFile, stub.handleCreateFile)
	server.Register(ipc.FuncSymlink, stub.handleSymlink)
	server.Register(ipc.FuncPivotRoot, stub.handlePivotRoot)
	server.Register(ipc.FuncUmount, stub.handleUmount)
	server.Register(ipc.FuncRmdir, stub.handleRmdir)
	server.Register(ipc.FuncUnlink, stub.handleUnlink)
	server.Register(ipc.FuncOpen, stub.handleOpen)

	if err := server.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "exec_env_stub: serve error: %v\n", err)
		os.Exit(1)
	}
}

type stubServer struct {
	mu    sync.Mutex
	procs map[int]*exec.Cmd
}

func (s *stubServer) handleExec(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
	p, err := ipc.DecodeExecRequest(payload)
	if err != nil {
		return nil, nil, err
	}

	if len(p.Argv) == 0 {
		return nil, nil, fmt.Errorf("empty argv")
	}

	cmd := exec.Command(p.Argv[0], p.Argv[1:]...)
	if p.Cwd != "" {
		cmd.Dir = p.Cwd
	}
	cmd.Env = ipc.ResolveEnv(p.ResetEnv, p.Env, os.Environ())

	cmd.SysProcAttr = &syscall.SysProcAttr{}
	if p.UnshareFlags != 0 {
		cmd.SysProcAttr.Cloneflags = uintptr(p.UnshareFlags)
	}
	if p.HasCreds {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: p.UID,
			Gid: p.GID,
		}
	}

	if len(fds) > 0 {
		cmd.Stdin = fds[0]
		if len(fds) > 1 {
			cmd.Stdout = fds[1]
		}
		if len(fds) > 2 {
			cmd.Stderr = fds[2]
		}
		if len(fds) > 3 {
			cmd.ExtraFiles = fds[3:]
		}
	} else {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Start(); err != nil {
		closeFDs(fds)
		return nil, nil, fmt.Errorf("start: %w", err)
	}

	closeFDs(fds)

	pid := cmd.Process.Pid
	s.mu.Lock()
	s.procs[pid] = cmd
	s.mu.Unlock()

	b, err := ipc.EncodeIntRequest(ipc.IntPayload{Value: int64(pid)})
	return b, nil, err
}

func (s *stubServer) handleWait(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
	closeFDs(fds)
	p, err := ipc.DecodeIntRequest(payload)
	if err != nil {
		return nil, nil, err
	}
	pid := int(p.Value)

	s.mu.Lock()
	cmd, ok := s.procs[pid]
	s.mu.Unlock()

	if !ok {
		return nil, nil, fmt.Errorf("unknown pid %d", pid)
	}

	werr := cmd.Wait()

	s.mu.Lock()
	delete(s.procs, pid)
	s.mu.Unlock()

	if werr != nil {
		if exitErr, ok := werr.(*exec.ExitError); ok {
			b, err := ipc.EncodeIntRequest(ipc.IntPayload{Value: int64(exitErr.ExitCode())})
			return b, nil, err
		}
		return nil, nil, werr
	}
	b, err := ipc.EncodeIntRequest(ipc.IntPayload{Value: 0})
	return b, nil, err
}

func (s *stubServer) handleMount(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
	closeFDs(fds)
	p, err := ipc.DecodeMountRequest(payload)
	if err != nil {
		return nil, nil, err
	}
	if err := syscall.Mount(p.Source, p.Target, p.FSType, uintptr(p.Flags), p.Data); err != nil {
		return nil, nil, fmt.Errorf("mount %s on %s type %s: %w", p.Source, p.Target, p.FSType, err)
	}
	return nil, nil, nil
}

func (s *stubServer) handleMkdir(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
	closeFDs(fds)
	p, err := ipc.DecodePathModeRequest(payload)
	if err != nil {
		return nil, nil, err
	}
	return nil, nil, os.MkdirAll(p.Path, os.FileMode(p.Mode))
}

func (s *stubServer) handleCreateFile(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
	closeFDs(fds)
	p, err := ipc.DecodePathModeRequest(payload)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(p.Path, os.O_CREATE|os.O_WRONLY, os.FileMode(p.Mode))
	if err != nil {
		return nil, nil, err
	}
	return nil, nil, f.Close()
}

func (s *stubServer) handleSymlink(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
	closeFDs(fds)
	p, err := ipc.DecodeSymlinkRequest(payload)
	if err != nil {
		return nil, nil, err
	}
	os.Remove(p.Linkpath)
	return nil, nil, os.Symlink(p.Target, p.Linkpath)
}

func (s *stubServer) handlePivotRoot(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
	closeFDs(fds)
	p, err := ipc.DecodePathRequest(payload)
	if err != nil {
		return nil, nil, err
	}
	rootfs := p.Path

	oldRoot := rootfs + "/run/old_root"
	os.MkdirAll(oldRoot, 0755)

	if err := syscall.Chdir(rootfs); err != nil {
		return nil, nil, fmt.Errorf("chdir to rootfs: %w", err)
	}

	if err := syscall.PivotRoot(".", "run/old_root"); err != nil {
		return nil, nil, fmt.Errorf("pivot_root: %w", err)
	}

	if err := syscall.Chroot("."); err != nil {
		return nil, nil, fmt.Errorf("chroot: %w", err)
	}

	if err := syscall.Chdir("/"); err != nil {
		return nil, nil, fmt.Errorf("chdir /: %w", err)
	}

	if err := syscall.Unmount("/run/old_root", syscall.MNT_DETACH); err != nil {
		return nil, nil, fmt.Errorf("unmount old root: %w", err)
	}

	os.Remove("/run/old_root")

	return nil, nil, nil
}

func (s *stubServer) handleUmount(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
	closeFDs(fds)
	p, err := ipc.DecodeUmountRequest(payload)
	if err != nil {
		return nil, nil, err
	}
	return nil, nil, syscall.Unmount(p.Target, int(p.Flags))
}

func (s *stubServer) handleRmdir(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
	closeFDs(fds)
	p, err := ipc.DecodePathRequest(payload)
	if err != nil {
		return nil, nil, err
	}
	return nil, nil, os.Remove(p.Path)
}

func (s *stubServer) handleUnlink(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
	closeFDs(fds)
	p, err := ipc.DecodePathRequest(payload)
	if err != nil {
		return nil, nil, err
	}
	return nil, nil, os.Remove(p.Path)
}

func (s *stubServer) handleOpen(payload []byte, fds []*os.File) ([]byte, []*os.File, error) {
	closeFDs(fds)
	p, err := ipc.DecodeOpenFileRequest(payload)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(p.Path, int(p.Flags), os.FileMode(p.Mode))
	if err != nil {
		return nil, nil, err
	}
	return nil, []*os.File{f}, nil
}

func closeFDs(fds []*os.File) {
	for _, f := range fds {
		if f != nil {
			f.Close()
		}
	}
}
