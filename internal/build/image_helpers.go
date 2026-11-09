package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"syscall"

	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// mountBindRO is the read-only bind-mount flag set used to stage input blobs
// into a container without copying.
const mountBindRO = syscall.MS_BIND | syscall.MS_RDONLY

// extractDebsRaw raw-extracts each package's .deb blob into dir via `dpkg-deb
// -x`, in the given mount namespace. Packages with no stored hash, or whose
// blob is absent from the store, are skipped. It is the shared raw-extract the
// rootfs layer build and the image assembly container both use to lay a set of
// .debs into a directory tree.
func extractDebsRaw(ctx context.Context, mountNS *container.MountNS, store *objstore.Store, pkgs []*index.Package, dir string, component log.Component) error {
	l := log.From(ctx, component)
	for _, pkg := range pkgs {
		if pkg.SHA256 == "" {
			continue
		}
		hash, err := objstore.NewHash(pkg.SHA256)
		if err != nil {
			continue
		}
		blobPath, err := store.Blobs.Path(hash)
		if err != nil {
			l.Warn("blob for %s absent: %v", pkg.Name, err)
			continue
		}
		if err := container.Run(mountNS, &container.ExecRequest{
			Argv: []string{"dpkg-deb", "-x", blobPath, dir},
			Env:  []string{"DEBIAN_FRONTEND=noninteractive"},
			Cwd:  "/",
		}); err != nil {
			return fmt.Errorf("extract %s: %w", pkg.Name, err)
		}
	}
	return nil
}

// harvestFileFromMountNS reads a file from inside the staging mount namespace
// at hostPath and streams it into the object store as a blob, returning its
// hash. The namespace hands back a real file descriptor over its IPC channel,
// so the bytes flow straight into the hash-on-write store with no in-container
// process and no copy out of the container.
func harvestFileFromMountNS(mountNS *container.MountNS, hostPath string, store *objstore.Store) (objstore.Hash, error) {
	f, err := mountNS.Open(hostPath, os.O_RDONLY, 0)
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("open %s: %w", hostPath, err)
	}
	defer f.Close()
	hash, err := store.Blobs.Store(f)
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("store %s: %w", hostPath, err)
	}
	return hash, nil
}

// sha256HexBytes returns the hex SHA-256 of data.
func sha256HexBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
