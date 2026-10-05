package container

import (
	"os"
)

// FsContext is a filesystem view — a namespace or host context where filesystem
// operations can be performed. BaseFsContext operates on the host filesystem
// directly; channel-backed implementations execute operations inside a stub's
// namespace via IPC.
type FsContext interface {
	// creation
	Mkdir(path string, mode uint32) error
	CreateFile(path string, mode uint32) error
	Symlink(target, linkpath string) error
	// deletion
	Rmdir(path string) error
	Unlink(path string) error
	// mounts
	Mount(source, target, fsType string, flags uintptr, data string) error
	Umount(target string, flags int) error
	// file I/O — mirrors os.OpenFile(name, flag, perm)
	Open(path string, flag int, perm uint32) (*os.File, error)
	// MkTempDir creates a new directory inside dir whose name begins with
	// prefix followed by 8 random hex characters. It retries on EEXIST.
	MkTempDir(dir, prefix string) (string, error)
}

// Compile-time check that BaseFsContext satisfies FsContext.
var _ FsContext = (*BaseFsContext)(nil)
var _ FsContext = (*RemoteExecEnv)(nil)
