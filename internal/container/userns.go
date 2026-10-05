package container

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"github.com/gardenlinux/glbx/internal/log"
)

// UserNS is a child user namespace. It does not own a mount namespace, so
// methods inherited from the embedded *RemoteExecEnv that operate on mounts
// (Mount, Mkdir, CreateFile, Symlink, Umount, PivotRoot) will fail at the
// kernel — they are surface-only artefacts of embedding. Use Exec/Wait/Close
// here, and layer a MountNS or Container on top for filesystem operations.
type UserNS struct {
	*RemoteExecEnv
}

type UserNSConfig struct {
	Ctx      context.Context
	Parent   ExecEnv
	StubPath string
	IDCount  uint32
}

func NewUserNS(cfg UserNSConfig) (*UserNS, error) {
	if cfg.IDCount == 0 {
		cfg.IDCount = 65536
	}

	l := log.From(cfg.Ctx, log.UserNS)

	remote, err := NewRemoteExecEnv(RemoteExecEnvConfig{
		Ctx:          cfg.Ctx,
		Component:    log.UserNS,
		Parent:       cfg.Parent,
		StubPath:     cfg.StubPath,
		UnshareFlags: syscall.CLONE_NEWUSER,
	})
	if err != nil {
		return nil, fmt.Errorf("create user namespace: %w", err)
	}

	l.Debug("stub pid=%d, reading subordinate ID ranges", remote.stubPID)

	uidRanges, err := GetSubordinateRanges(true)
	if err != nil {
		remote.Close()
		return nil, fmt.Errorf("get subordinate uid ranges: %w", err)
	}
	gidRanges, err := GetSubordinateRanges(false)
	if err != nil {
		remote.Close()
		return nil, fmt.Errorf("get subordinate gid ranges: %w", err)
	}

	parentUIDMap, err := ReadProcIDMap(os.Getpid(), true)
	if err != nil {
		parentUIDMap = []IDMapping{{Inner: 0, Outer: 0, Count: 4294967295}}
	}
	parentGIDMap, err := ReadProcIDMap(os.Getpid(), false)
	if err != nil {
		parentGIDMap = []IDMapping{{Inner: 0, Outer: 0, Count: 4294967295}}
	}

	currentUID := uint32(os.Getuid())
	currentGID := uint32(os.Getgid())

	subUIDCount := cfg.IDCount - 1
	subUIDMappings, err := ComputeIDMappings(uidRanges, parentUIDMap, subUIDCount)
	if err != nil {
		remote.Close()
		return nil, fmt.Errorf("compute uid mappings: %w", err)
	}
	for i := range subUIDMappings {
		subUIDMappings[i].Inner += 1
	}
	uidMappings := append([]IDMapping{{Inner: 0, Outer: currentUID, Count: 1}}, subUIDMappings...)

	subGIDCount := cfg.IDCount - 1
	subGIDMappings, err := ComputeIDMappings(gidRanges, parentGIDMap, subGIDCount)
	if err != nil {
		remote.Close()
		return nil, fmt.Errorf("compute gid mappings: %w", err)
	}
	for i := range subGIDMappings {
		subGIDMappings[i].Inner += 1
	}
	gidMappings := append([]IDMapping{{Inner: 0, Outer: currentGID, Count: 1}}, subGIDMappings...)

	l.Debug("applying uid_map: %d entries, gid_map: %d entries", len(uidMappings), len(gidMappings))

	if err := ApplyIDMappings(remote.stubPID, uidMappings, gidMappings); err != nil {
		remote.Close()
		return nil, fmt.Errorf("apply id mappings: %w", err)
	}

	l.Debug("ID mappings applied successfully")

	nsID := readNSInode(remote.stubPID, "user")
	l.Debug("created user namespace (stub pid=%d, user:[%s])", remote.stubPID, nsID)

	return &UserNS{RemoteExecEnv: remote}, nil
}
