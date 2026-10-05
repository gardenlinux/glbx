package container

import (
	"os"
)

type Credentials struct {
	UID uint32
	GID uint32
}

type ExecRequest struct {
	Argv []string
	Cwd  string
	// Env entries overlay onto the inherited base env: keys present in the
	// base are overridden in place, new keys are appended. The base is the
	// calling process's os.Environ() unless ResetEnv is true.
	Env []string
	// ResetEnv replaces the inherited base env with a single-entry minimal
	// env (PATH = ipc.DefaultPATH) before applying Env. Use to start a child
	// with a clean slate (e.g. inside a freshly-pivoted rootfs).
	ResetEnv     bool
	Credentials  *Credentials
	UnshareFlags uintptr
	FDs          []*os.File
}

type ExecEnv interface {
	Exec(req *ExecRequest) (int, error)
	Wait(pid int) (int, error)
	Close() error
}
