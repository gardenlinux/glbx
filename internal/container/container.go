package container

import (
	"context"
	"fmt"
	"syscall"

	"github.com/gardenlinux/glbx/internal/log"
)

type Container struct {
	*RemoteExecEnv
	rootfs string
}

type ContainerConfig struct {
	Ctx      context.Context
	Parent   ExecEnv
	StubPath string
	Rootfs   string
}

func NewContainer(cfg ContainerConfig) (*Container, error) {
	if cfg.Rootfs == "" {
		return nil, fmt.Errorf("rootfs path required")
	}
	if cfg.Ctx == nil {
		cfg.Ctx = context.Background()
	}

	l := log.From(cfg.Ctx, log.Container)
	l.Debug("unsharing mount+pid namespaces")
	remote, err := NewRemoteExecEnv(RemoteExecEnvConfig{
		Ctx:          cfg.Ctx,
		Component:    log.Container,
		Parent:       cfg.Parent,
		StubPath:     cfg.StubPath,
		UnshareFlags: syscall.CLONE_NEWNS | syscall.CLONE_NEWPID,
		Credentials:  &Credentials{UID: 0, GID: 0},
		ResetEnv:     true,
	})
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}

	pidNS := readNSInode(remote.stubPID, "pid")
	mntNS := readNSInode(remote.stubPID, "mnt")
	l.Debug("created container (stub pid=%d, pid:[%s], mnt:[%s])", remote.stubPID, pidNS, mntNS)

	c := &Container{
		RemoteExecEnv: remote,
		rootfs:        cfg.Rootfs,
	}

	l.Debug("setting up mounts (proc, sys, dev, tmp, ...)")
	if err := c.setupMounts(); err != nil {
		remote.Close()
		return nil, fmt.Errorf("setup container mounts: %w", err)
	}

	l.Debug("pivot_root into %s", cfg.Rootfs)
	if err := c.PivotRoot(c.rootfs); err != nil {
		remote.Close()
		return nil, fmt.Errorf("pivot root: %w", err)
	}

	return c, nil
}

func (c *Container) setupMounts() error {
	rootfs := c.rootfs

	// Make the entire mount tree slave so we receive propagation from parent
	// but don't leak our mounts back. This allows the parent MountNS to
	// bind-mount files under rootfs and have them appear inside the container.
	if err := c.Mount("", "/", "", syscall.MS_REC|syscall.MS_SLAVE, ""); err != nil {
		// Fallback: try private (no propagation, but at least functional)
		if err2 := c.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err2 != nil {
			return fmt.Errorf("make mount tree slave/private: slave: %w, private: %v", err, err2)
		}
	}

	if err := c.Mount(rootfs, rootfs, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		if err2 := c.Mount(rootfs, rootfs, "", syscall.MS_BIND, ""); err2 != nil {
			return fmt.Errorf("bind mount rootfs: %w (also tried without MS_REC: %v)", err, err2)
		}
	}

	procPath := rootfs + "/proc"
	c.Mkdir(procPath, 0755)
	if err := c.Mount("proc", procPath, "proc", 0, ""); err != nil {
		return fmt.Errorf("mount proc: %w", err)
	}

	sysPath := rootfs + "/sys"
	c.Mkdir(sysPath, 0755)
	if err := c.Mount("sysfs", sysPath, "sysfs", syscall.MS_RDONLY, ""); err != nil {
		if err2 := c.Mount("tmpfs", sysPath, "tmpfs", syscall.MS_RDONLY, "size=0"); err2 != nil {
			return fmt.Errorf("mount sys: sysfs: %w, tmpfs fallback: %v", err, err2)
		}
	}

	devPath := rootfs + "/dev"
	c.Mkdir(devPath, 0755)
	if err := c.Mount("tmpfs", devPath, "tmpfs", 0, "mode=0755"); err != nil {
		return fmt.Errorf("mount dev tmpfs: %w", err)
	}

	devNodes := []string{"null", "zero", "full", "random", "urandom", "tty"}
	for _, node := range devNodes {
		nodePath := devPath + "/" + node
		c.CreateFile(nodePath, 0666)
		if err := c.Mount("/dev/"+node, nodePath, "", syscall.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind mount /dev/%s: %w", node, err)
		}
	}

	ptsPath := devPath + "/pts"
	c.Mkdir(ptsPath, 0755)
	if err := c.Mount("devpts", ptsPath, "devpts", 0, "newinstance,ptmxmode=0666"); err != nil {
		return fmt.Errorf("mount devpts: %w", err)
	}

	if err := c.Symlink("/proc/self/fd/0", devPath+"/stdin"); err != nil {
		return fmt.Errorf("symlink dev/stdin: %w", err)
	}
	if err := c.Symlink("/proc/self/fd/1", devPath+"/stdout"); err != nil {
		return fmt.Errorf("symlink dev/stdout: %w", err)
	}
	if err := c.Symlink("/proc/self/fd/2", devPath+"/stderr"); err != nil {
		return fmt.Errorf("symlink dev/stderr: %w", err)
	}
	if err := c.Symlink("/proc/self/fd", devPath+"/fd"); err != nil {
		return fmt.Errorf("symlink dev/fd: %w", err)
	}
	if err := c.Symlink("pts/ptmx", devPath+"/ptmx"); err != nil {
		return fmt.Errorf("symlink dev/ptmx: %w", err)
	}

	runPath := rootfs + "/run"
	c.Mkdir(runPath, 0755)
	if err := c.Mount("tmpfs", runPath, "tmpfs", 0, "mode=0755"); err != nil {
		return fmt.Errorf("mount /run tmpfs: %w", err)
	}

	tmpPath := rootfs + "/tmp"
	c.Mkdir(tmpPath, 01777)
	if err := c.Mount("tmpfs", tmpPath, "tmpfs", 0, "mode=1777"); err != nil {
		return fmt.Errorf("mount /tmp tmpfs: %w", err)
	}

	return nil
}
