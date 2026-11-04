package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/signing"
)

// signingServiceDir is the fixed in-container directory under which each signing
// key's socket is mounted. The full path /glbx/signing-service/<key_name> is
// what the PKCS#11 forwarding module connects to, named by the GLBX_SIGN_SOCKET
// environment variable. It sits outside the directories the container overmounts
// with fresh tmpfs (/run, /tmp, /proc, /sys, /dev), so the bind survives.
const signingServiceDir = "/glbx/signing-service"

// signingMount describes one live signing key wired into a build: a host-side
// socket, served by an in-process signing service, bind-mounted to a container
// path. The service answers public-key, certificate, and signing requests over
// that socket; neither the key nor the certificate is ever a file in the
// container.
type signingMount struct {
	keyName    string
	hostSocket string // host path the signing service listens on
	socketMnt  string // container path the socket is bind-mounted to
}

// setupSigning launches a signing service per configured key on host sockets and
// records the resulting mounts on the build for setupBuildEnv to bind in. The
// private keys are read only here, on the host, and never reach the container.
// The returned stop function halts the services and removes the host socket dir.
func (s *DebianPkgBuild) setupSigning(ctx context.Context) (func(), error) {
	if len(s.signingKeys) == 0 {
		return func() {}, nil
	}
	l := log.From(ctx, log.Build)
	confDir := filepath.Dir(filepath.Dir(s.PkgDir))

	scratchRoot := s.ScratchDir
	if scratchRoot == "" {
		scratchRoot = filepath.Join(objstore.DefaultRoot(), "tmp")
	}
	if err := os.MkdirAll(scratchRoot, 0755); err != nil {
		return nil, fmt.Errorf("mkdir scratch root: %w", err)
	}
	hostDir, err := os.MkdirTemp(scratchRoot, "glbx-sign-")
	if err != nil {
		return nil, fmt.Errorf("mkdir signing dir: %w", err)
	}

	svcCtx, cancel := context.WithCancel(ctx)
	stop := func() {
		cancel()
		os.RemoveAll(hostDir)
	}

	for _, k := range s.signingKeys {
		backend, err := signing.NewKeyFileBackend(
			filepath.Join(confDir, k.PrivateKeyFile),
			filepath.Join(confDir, k.CertFile),
		)
		if err != nil {
			stop()
			return nil, fmt.Errorf("signing key %q: %w", k.KeyName, err)
		}
		hostSocket := filepath.Join(hostDir, k.KeyName+".sock")
		go func() {
			if err := signing.Serve(svcCtx, hostSocket, backend); err != nil {
				l.Warn("signing service %q: %v", k.KeyName, err)
			}
		}()
		// Wait for the socket to exist before returning: it is bind-mounted into
		// the container immediately after setup, and a bind-mount captures the
		// source inode at mount time, so the socket (created and chmod'd by the
		// goroutine above) must be in place first.
		if err := waitForSocket(hostSocket); err != nil {
			stop()
			return nil, fmt.Errorf("signing service %q: %w", k.KeyName, err)
		}
		s.signingMnts = append(s.signingMnts, signingMount{
			keyName:    k.KeyName,
			hostSocket: hostSocket,
			socketMnt:  signingServiceDir + "/" + k.KeyName,
		})
	}
	return stop, nil
}

// waitForSocket blocks until path is a socket with its connect-permission bits
// set (0666), or times out. The signing service creates the socket and then
// chmods it from a goroutine; callers must not bind-mount it before both steps
// complete, since a bind-mount captures the inode — and its mode — at mount time.
func waitForSocket(path string) error {
	for i := 0; i < 500; i++ {
		if fi, err := os.Stat(path); err == nil &&
			fi.Mode()&os.ModeSocket != 0 && fi.Mode().Perm() == 0666 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("signing socket %s did not become ready", path)
}

// signingEnv returns the environment variables a signing-enabled build needs so
// the PKCS#11 forwarding module can find its socket. The kernel package uses a
// single key, so GLBX_SIGN_SOCKET names that one socket. GLBX_SIGN_DEBUG is
// forwarded when set in glbx's own environment, so the in-container forwarding
// module's diagnostics can be enabled without rebuilding it.
func signingEnv(mounts []signingMount) []string {
	if len(mounts) == 0 {
		return nil
	}
	env := []string{"GLBX_SIGN_SOCKET=" + mounts[0].socketMnt}
	if dbg := os.Getenv("GLBX_SIGN_DEBUG"); dbg != "" {
		env = append(env, "GLBX_SIGN_DEBUG="+dbg)
	}
	return env
}

// bindSigningMounts binds each key's host socket into the container's mount tree
// at /glbx/signing-service/<key_name>, so the forwarding module can reach the
// host signing service. The bind runs in the parent mount namespace (where the
// host-side socket path is still visible as a mount source) and targets a
// directory the container does not later overmount with a fresh tmpfs.
// rootfsPath is the container root in the mount namespace.
func (s *DebianPkgBuild) bindSigningMounts(fs *container.RemoteExecEnv, rootfsPath string) error {
	for _, m := range s.signingMnts {
		socketTarget := rootfsPath + m.socketMnt
		if err := mkdirAll(fs, filepath.Dir(socketTarget)); err != nil {
			return fmt.Errorf("mkdir for signing socket: %w", err)
		}
		if err := fs.CreateFile(socketTarget, 0600); err != nil {
			return fmt.Errorf("create signing socket target: %w", err)
		}
		if err := fs.Mount(m.hostSocket, socketTarget, "", syscall.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind signing socket: %w", err)
		}
	}
	return nil
}

// mkdirAll creates path and any missing parents, which the mount context offers
// only single-level Mkdir for. Existing directories are not an error.
func mkdirAll(fs *container.RemoteExecEnv, path string) error {
	var cur string
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" {
			continue
		}
		cur += "/" + part
		if err := fs.Mkdir(cur, 0755); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}
