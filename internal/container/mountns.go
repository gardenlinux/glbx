package container

import (
	"context"
	"fmt"
	"syscall"

	"github.com/gardenlinux/glbx/internal/log"
)

// MountNS is a child mount namespace. At construction time it inherits the
// parent's mount tree verbatim — every path visible to the host (including the
// object store, working dirs, /home, etc.) is initially visible inside the
// namespace at the same path. Subsequent Mount/Umount calls rearrange this
// view; callers that have done so must not assume host paths remain visible.
//
// In particular, host paths passed to Mount, Mkdir, Symlink, Umount, and the
// bind-mount source argument are interpreted in the namespace's current mount
// tree, not the host's. Callers that compute paths against the host root and
// then rearrange the mount tree must either translate paths or stage all
// bind-mount sources before the rearrangement.
type MountNS struct {
	*RemoteExecEnv
}

type MountNSConfig struct {
	Ctx      context.Context
	Parent   ExecEnv
	StubPath string
}

// NewMountNS spawns a stub in a fresh mount namespace (CLONE_NEWNS only) under
// cfg.Parent. The new namespace inherits the parent's mount tree at creation
// time — see the MountNS type comment for the path-visibility contract.
func NewMountNS(cfg MountNSConfig) (*MountNS, error) {
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}
	l := log.From(cfg.Ctx, log.MountNS)

	remote, err := NewRemoteExecEnv(RemoteExecEnvConfig{
		Ctx:          cfg.Ctx,
		Component:    log.MountNS,
		Parent:       cfg.Parent,
		StubPath:     cfg.StubPath,
		UnshareFlags: syscall.CLONE_NEWNS,
		Credentials:  &Credentials{UID: 0, GID: 0},
	})
	if err != nil {
		return nil, fmt.Errorf("create mount namespace: %w", err)
	}

	nsID := readNSInode(remote.stubPID, "mnt")
	l.Debug("created mount namespace (stub pid=%d, mnt:[%s])", remote.stubPID, nsID)

	return &MountNS{RemoteExecEnv: remote}, nil
}
