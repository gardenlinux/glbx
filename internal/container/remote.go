package container

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/gardenlinux/glbx/internal/ipc"
	"github.com/gardenlinux/glbx/internal/log"
)

// RemoteExecEnv is an ExecEnv and FsContext that drives an exec_env_stub child
// over an IPC socket. It provides the IPC transport (Exec/Wait/Close) plus all
// filesystem ops (Mount/Mkdir/CreateFile/Symlink/Umount/PivotRoot/Rmdir/Unlink/Open)
// implemented by the stub. Higher-level wrappers (MountNS, Container, UserNS)
// embed *RemoteExecEnv and inherit these methods.
type RemoteExecEnv struct {
	parent    ExecEnv
	stubPID   int
	client    *ipc.Client
	stubPath  string
	ctx       context.Context
	component log.Component
}

type RemoteExecEnvConfig struct {
	Ctx          context.Context
	Component    log.Component
	Parent       ExecEnv
	StubPath     string
	UnshareFlags uintptr
	Credentials  *Credentials
	// ResetEnv, when true, launches the stub with a clean minimal env
	// (PATH = ipc.DefaultPATH, nothing else). When false (default) the stub
	// inherits the parent ExecEnv's env. Only Container opts in — UserNS and
	// MountNS keep the host env.
	ResetEnv bool
}

// StubPath returns the path to the exec_env_stub binary. It looks alongside
// the current executable; if no file exists there it falls back to the bare
// name so $PATH resolution gets a chance.
func StubPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "exec_env_stub"
	}
	stub := filepath.Join(filepath.Dir(exe), "exec_env_stub")
	if _, err := os.Stat(stub); err == nil {
		return stub
	}
	return "exec_env_stub"
}

func NewRemoteExecEnv(cfg RemoteExecEnvConfig) (*RemoteExecEnv, error) {
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
	stubPath := cfg.StubPath
	if stubPath == "" {
		stubPath = StubPath()
	}

	hostFD, childFD, err := ipc.NewSocketPair()
	if err != nil {
		return nil, fmt.Errorf("create socket pair: %w", err)
	}

	req := &ExecRequest{
		Argv:         []string{stubPath},
		ResetEnv:     cfg.ResetEnv,
		UnshareFlags: cfg.UnshareFlags,
		Credentials:  cfg.Credentials,
		FDs:          []*os.File{os.Stdin, os.Stdout, os.Stderr, childFD},
	}

	pid, err := cfg.Parent.Exec(req)
	childFD.Close()
	if err != nil {
		hostFD.Close()
		return nil, fmt.Errorf("start stub: %w", err)
	}

	client := ipc.NewClient(hostFD)

	return &RemoteExecEnv{
		parent:    cfg.Parent,
		stubPID:   pid,
		client:    client,
		stubPath:  stubPath,
		ctx:       cfg.Ctx,
		component: cfg.Component,
	}, nil
}

func (r *RemoteExecEnv) Exec(req *ExecRequest) (int, error) {
	payload, err := ipc.EncodeExecRequest(ipc.ExecPayload{
		Argv:         req.Argv,
		Cwd:          req.Cwd,
		Env:          req.Env,
		ResetEnv:     req.ResetEnv,
		HasCreds:     req.Credentials != nil,
		UID:          credUID(req.Credentials),
		GID:          credGID(req.Credentials),
		UnshareFlags: uint64(req.UnshareFlags),
	})
	if err != nil {
		return 0, fmt.Errorf("remote exec: encode: %w", err)
	}
	resp, _, err := r.client.Call(ipc.FuncExec, payload, req.FDs)
	if err != nil {
		return 0, fmt.Errorf("remote exec: %w", err)
	}
	reply, err := ipc.DecodeIntRequest(resp)
	if err != nil {
		return 0, err
	}
	return int(reply.Value), nil
}

func (r *RemoteExecEnv) Wait(pid int) (int, error) {
	payload, err := ipc.EncodeIntRequest(ipc.IntPayload{Value: int64(pid)})
	if err != nil {
		return -1, fmt.Errorf("remote wait: encode: %w", err)
	}
	resp, _, err := r.client.Call(ipc.FuncWait, payload, nil)
	if err != nil {
		return -1, fmt.Errorf("remote wait: %w", err)
	}
	reply, err := ipc.DecodeIntRequest(resp)
	if err != nil {
		return -1, err
	}
	return int(reply.Value), nil
}

func (r *RemoteExecEnv) Close() error {
	r.client.Close()
	_, err := r.parent.Wait(r.stubPID)
	return err
}

func (r *RemoteExecEnv) Mount(source, target, fsType string, flags uintptr, data string) error {
	l := log.From(r.ctx, r.component)
	if fsType != "" {
		l.Debug("mount %s on %s (type=%s)", source, target, fsType)
	} else if flags&syscall.MS_BIND != 0 {
		l.Debug("bind-mount %s → %s", source, target)
	} else {
		l.Debug("mount %s on %s (flags=0x%x)", source, target, flags)
	}
	payload, err := ipc.EncodeMountRequest(ipc.MountPayload{
		Source: source,
		Target: target,
		FSType: fsType,
		Flags:  uint64(flags),
		Data:   data,
	})
	if err != nil {
		return fmt.Errorf("mount: encode: %w", err)
	}
	_, _, err = r.client.Call(ipc.FuncMount, payload, nil)
	return err
}

func (r *RemoteExecEnv) Mkdir(path string, mode uint32) error {
	payload, err := ipc.EncodePathModeRequest(ipc.PathModePayload{Path: path, Mode: mode})
	if err != nil {
		return fmt.Errorf("mkdir: encode: %w", err)
	}
	_, _, err = r.client.Call(ipc.FuncMkdir, payload, nil)
	return err
}

func (r *RemoteExecEnv) CreateFile(path string, mode uint32) error {
	payload, err := ipc.EncodePathModeRequest(ipc.PathModePayload{Path: path, Mode: mode})
	if err != nil {
		return fmt.Errorf("createFile: encode: %w", err)
	}
	_, _, err = r.client.Call(ipc.FuncCreateFile, payload, nil)
	return err
}

func (r *RemoteExecEnv) Symlink(target, linkpath string) error {
	payload, err := ipc.EncodeSymlinkRequest(ipc.SymlinkPayload{Target: target, Linkpath: linkpath})
	if err != nil {
		return fmt.Errorf("symlink: encode: %w", err)
	}
	_, _, err = r.client.Call(ipc.FuncSymlink, payload, nil)
	return err
}

func (r *RemoteExecEnv) Umount(target string, flags int) error {
	payload, err := ipc.EncodeUmountRequest(ipc.UmountPayload{Target: target, Flags: int64(flags)})
	if err != nil {
		return fmt.Errorf("umount: encode: %w", err)
	}
	_, _, err = r.client.Call(ipc.FuncUmount, payload, nil)
	return err
}

func (r *RemoteExecEnv) PivotRoot(rootfs string) error {
	payload, err := ipc.EncodePathRequest(ipc.PathPayload{Path: rootfs})
	if err != nil {
		return fmt.Errorf("pivotRoot: encode: %w", err)
	}
	_, _, err = r.client.Call(ipc.FuncPivotRoot, payload, nil)
	return err
}

func (r *RemoteExecEnv) Rmdir(path string) error {
	payload, err := ipc.EncodePathRequest(ipc.PathPayload{Path: path})
	if err != nil {
		return fmt.Errorf("rmdir: encode: %w", err)
	}
	_, _, err = r.client.Call(ipc.FuncRmdir, payload, nil)
	return err
}

func (r *RemoteExecEnv) Unlink(path string) error {
	payload, err := ipc.EncodePathRequest(ipc.PathPayload{Path: path})
	if err != nil {
		return fmt.Errorf("unlink: encode: %w", err)
	}
	_, _, err = r.client.Call(ipc.FuncUnlink, payload, nil)
	return err
}

func (r *RemoteExecEnv) Open(path string, flag int, perm uint32) (*os.File, error) {
	payload, err := ipc.EncodeOpenFileRequest(ipc.OpenFilePayload{
		Path:  path,
		Flags: int32(flag),
		Mode:  perm,
	})
	if err != nil {
		return nil, fmt.Errorf("open: encode: %w", err)
	}
	_, fds, err := r.client.Call(ipc.FuncOpen, payload, nil)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if len(fds) != 1 {
		for _, f := range fds {
			f.Close()
		}
		return nil, fmt.Errorf("open %s: expected 1 fd in response, got %d", path, len(fds))
	}
	return fds[0], nil
}

func (r *RemoteExecEnv) MkTempDir(dir, prefix string) (string, error) {
	return doMkTempDir(dir, prefix, func(path string) error {
		return r.Mkdir(path, 0700)
	})
}

func credUID(c *Credentials) uint32 {
	if c == nil {
		return 0
	}
	return c.UID
}

func credGID(c *Credentials) uint32 {
	if c == nil {
		return 0
	}
	return c.GID
}
