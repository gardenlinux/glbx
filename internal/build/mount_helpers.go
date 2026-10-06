package build

import (
	"fmt"
	"syscall"

	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// setupDirsInMountNS creates required directories inside the mount namespace.
// Only the bare-minimum dpkg infrastructure dirs the install flow itself
// needs to write into are created here. All other layout (/etc, /var/...,
// etc.) comes from the extracted packages (base-files etc.).
func setupDirsInMountNS(mountNS *container.MountNS, rootfs string) error {
	dirs := []string{
		"var/lib/dpkg", "var/lib/dpkg/info", "var/lib/dpkg/updates", "var/lib/dpkg/triggers",
		"tmp", "run", "proc", "sys", "dev",
	}
	for _, d := range dirs {
		if err := mountNS.Mkdir(rootfs+"/"+d, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	for _, f := range []string{"var/lib/dpkg/status", "var/lib/dpkg/available"} {
		if err := mountNS.CreateFile(rootfs+"/"+f, 0644); err != nil {
			return fmt.Errorf("create %s: %w", f, err)
		}
	}
	return nil
}

// setupLocalRepoInMountNS bind-mounts each .deb blob from the objstore into
// the namespace at /pkgs/<name>_<ver>_<arch>.deb so the in-container
// `dpkg --unpack /pkgs/*.deb` step can find them. No apt repo metadata is
// written — installation is dpkg-only.
func setupLocalRepoInMountNS(mountNS *container.MountNS, store *objstore.Store, packages []*index.Package, rootfs string) error {
	if err := mountNS.Mkdir(rootfs+"/pkgs", 0755); err != nil {
		return fmt.Errorf("mkdir /pkgs: %w", err)
	}

	for _, pkg := range packages {
		if pkg.SHA256 == "" {
			continue
		}
		hash, err := objstore.NewHash(pkg.SHA256)
		if err != nil {
			continue
		}
		blobPath, err := store.Blobs.Path(hash)
		if err != nil || !store.Blobs.Has(hash) {
			continue
		}
		debName := fmt.Sprintf("%s_%s_%s.deb", pkg.Name, pkg.Version, pkg.Architecture)
		targetPath := rootfs + "/pkgs/" + debName

		if err := mountNS.CreateFile(targetPath, 0644); err != nil {
			continue
		}
		if err := mountNS.Mount(blobPath, targetPath, "", syscall.MS_BIND|syscall.MS_RDONLY, ""); err != nil {
			continue
		}
	}

	return nil
}
