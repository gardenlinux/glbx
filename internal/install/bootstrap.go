package install

import (
	"context"
	"fmt"
	"os"
	"sort"
	"syscall"

	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

type debMount struct {
	targetPath string
	debPath    string
}

// Bootstrap creates a minimal container from essential packages (transitive deps included):
//  1. Creates a tmpdir + tmpfs inside the MountNS
//  2. Resolves essential packages from the index
//  3. dpkg-deb --extract each resolved pkg (loop, alphabetical order)
//  4. Sets up dpkg infrastructure dirs
//  5. Bind-mounts all .debs into rootfs/pkgs/ (BEFORE Container creation)
//  6. Creates a Container (PID ns + pivot_root)
//  7. dpkg --unpack --force-depends (bypass Pre-Depends cycles)
//  8. dpkg --configure --pending (proper dependency ordering)
//  9. Clean up bind-mounts
//
// Returns the container, the rootfs path, and a cleanup function.
func Bootstrap(ctx context.Context, mountNS *container.MountNS, store *objstore.Store, idx *index.Index, arch string, stubPath string) (*container.Container, string, func(), error) {
	l := log.From(ctx, log.Build)

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, "", nil, fmt.Errorf("open devnull: %w", err)
	}
	defer devNull.Close()

	// 1. Create work area
	workPath, err := mountNS.MkTempDir("/tmp", "glbx-bootstrap-")
	if err != nil {
		return nil, "", nil, fmt.Errorf("mktempdir: %w", err)
	}
	if err := mountNS.Mount("tmpfs", workPath, "tmpfs", 0, "size=2g"); err != nil {
		return nil, "", nil, fmt.Errorf("mount tmpfs on %s: %w", workPath, err)
	}

	rootfsPath := workPath + "/rootfs"
	if err := mountNS.Mkdir(rootfsPath, 0755); err != nil {
		return nil, "", nil, fmt.Errorf("mkdir rootfs: %w", err)
	}

	// 2. Resolve essential packages
	var coreNames []string
	for _, pkg := range idx.EssentialPackages() {
		coreNames = append(coreNames, pkg.Name)
	}

	l.Info("resolving %d essential packages", len(coreNames))
	corePkgs, err := Resolve(idx, arch, coreNames)
	if err != nil {
		return nil, "", nil, fmt.Errorf("resolve core: %w", err)
	}
	l.Info("resolved to %d packages (with transitive deps)", len(corePkgs))

	// 3. Extract each package via dpkg-deb --extract (alphabetical order)
	sort.Slice(corePkgs, func(i, j int) bool {
		return corePkgs[i].Name < corePkgs[j].Name
	})

	l.Info("extracting %d packages into rootfs", len(corePkgs))
	for _, pkg := range corePkgs {
		if pkg.SHA256 == "" {
			continue
		}
		hash, err := objstore.NewHash(pkg.SHA256)
		if err != nil {
			l.Warn("skipping %s: blob not in store", pkg.Name)
			continue
		}
		store.EnsureBlob(hash) // pull-through by digest if not local
		if !store.Blobs.Has(hash) {
			l.Warn("skipping %s: blob not in store", pkg.Name)
			continue
		}
		blobPath := store.Blobs.Path(hash)
		if err := container.Run(mountNS, &container.ExecRequest{
			Argv: []string{"dpkg-deb", "--extract", blobPath, rootfsPath},
			Env:  []string{"DEBIAN_FRONTEND=noninteractive"},
			Cwd:  "/",
			FDs:  []*os.File{devNull, os.Stdout, os.Stderr},
		}); err != nil {
			return nil, "", nil, fmt.Errorf("extract %s: %w", pkg.Name, err)
		}
	}

	// 4. Setup dpkg infrastructure directories
	if err := setupDpkgDirs(mountNS, rootfsPath); err != nil {
		return nil, "", nil, fmt.Errorf("setup dpkg dirs: %w", err)
	}

	// 5. Make rootfs a shared mount point so mounts propagate into Container
	//    (Container uses MS_SLAVE, so parent→child propagation works)
	if err := mountNS.Mount(rootfsPath, rootfsPath, "", syscall.MS_BIND, ""); err != nil {
		return nil, "", nil, fmt.Errorf("bind rootfs: %w", err)
	}
	if err := mountNS.Mount("", rootfsPath, "", syscall.MS_SHARED, ""); err != nil {
		return nil, "", nil, fmt.Errorf("make rootfs shared: %w", err)
	}

	// 6. Create container
	cont, err := container.NewContainer(container.ContainerConfig{
		Ctx:      ctx,
		Parent:   mountNS,
		StubPath: stubPath,
		Rootfs:   rootfsPath,
	})
	if err != nil {
		return nil, "", nil, fmt.Errorf("create container: %w", err)
	}

	cleanup := func() {
		cont.Close()
	}

	// 7. Install essential packages via InstallResolved (unpack + configure)
	if err := InstallResolved(ctx, cont, mountNS, store, rootfsPath, corePkgs); err != nil {
		return cont, rootfsPath, cleanup, fmt.Errorf("install essential packages: %w", err)
	}

	return cont, rootfsPath, cleanup, nil
}

func setupDpkgDirs(mountNS *container.MountNS, rootfs string) error {
	dirs := []string{
		"var/lib/dpkg",
		"var/lib/dpkg/info",
		"var/lib/dpkg/updates",
		"var/lib/dpkg/triggers",
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
