package install

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"syscall"

	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// InstallResolved bind-mounts .deb blobs (read-only) into rootfs/pkgs/ via the
// parent MountNS (mounts propagate into the Container via MS_SHARED/MS_SLAVE),
// runs `dpkg --install` with ALL debs in a single invocation inside the
// Container, then cleans up bind-mounts.
// dpkg handles dependency ordering internally when given the complete set.
func InstallResolved(ctx context.Context, cont *container.Container, mountNS *container.MountNS, store *objstore.Store, rootfsPath string, pkgs []*index.Package) error {
	l := log.From(ctx, log.Build)

	pkgsDir := rootfsPath + "/pkgs"
	mountNS.Mkdir(pkgsDir, 0755)

	var mounts []debMount

	for _, pkg := range pkgs {
		if pkg.SHA256 == "" {
			continue
		}
		hash, err := objstore.NewHash(pkg.SHA256)
		if err != nil {
			return fmt.Errorf("missing blob for %s (%s)", pkg.Name, pkg.SHA256)
		}
		// Materialize the .deb locally, pulling from the remote by digest on a
		// miss. A path error means it is unavailable (locally and remotely).
		blobPath, err := store.Blobs.Path(hash)
		if err != nil || !store.Blobs.Has(hash) {
			return fmt.Errorf("missing blob for %s (%s)", pkg.Name, pkg.SHA256)
		}
		debName := fmt.Sprintf("%s_%s_%s.deb", pkg.Name, pkg.Version, pkg.Architecture)
		targetPath := pkgsDir + "/" + debName

		if err := mountNS.CreateFile(targetPath, 0644); err != nil {
			cleanupDebMounts(mountNS, mounts)
			return fmt.Errorf("create %s: %w", debName, err)
		}
		if err := mountNS.Mount(blobPath, targetPath, "", syscall.MS_BIND|syscall.MS_RDONLY, ""); err != nil {
			cleanupDebMounts(mountNS, mounts)
			return fmt.Errorf("bind-mount %s: %w", debName, err)
		}
		mounts = append(mounts, debMount{targetPath: targetPath, debPath: "/pkgs/" + debName})
	}

	if len(mounts) == 0 {
		return nil
	}

	var debPaths []string
	for _, m := range mounts {
		debPaths = append(debPaths, m.debPath)
	}
	sort.Strings(debPaths)

	// Two-phase: unpack all with --force-depends, then configure in dependency
	// order. Pre-Depends requires deps to be configured BEFORE the dependent is
	// unpacked, which makes single-pass batch install impossible when starting
	// from an empty dpkg database. The standard approach (debootstrap, our own
	// Bootstrap) is: force-unpack to get all files on disk, then --configure
	// --pending which validates all deps are present and configures in correct
	// topological order. If a dep is genuinely missing, configure fails.
	unpackArgv := append([]string{"dpkg", "--unpack", "--force-depends"}, debPaths...)
	l.Info("dpkg --unpack --force-depends (%d packages)", len(debPaths))
	if err := execInContainer(ctx, cont, unpackArgv); err != nil {
		cleanupDebMounts(mountNS, mounts)
		return err
	}

	// --force-confnew silently replaces on-disk conffiles whose content matches
	// neither the old nor the new packaged md5. Required because install_check
	// layers a locally-built closure on top of a lockfile-bootstrapped rootfs;
	// when a conffile (e.g. base-files' /etc/debian_version) changes content
	// across two versions of the same package, dpkg would otherwise prompt and
	// die on EOF (stdin is /dev/null). DEBIAN_FRONTEND=noninteractive does not
	// suppress dpkg's own conffile prompt — only debconf-driven questions.
	l.Info("dpkg --configure --pending")
	if err := execInContainer(ctx, cont, []string{"dpkg", "--force-confnew", "--configure", "--pending"}); err != nil {
		cleanupDebMounts(mountNS, mounts)
		return err
	}

	cleanupDebMounts(mountNS, mounts)
	return nil
}

func cleanupDebMounts(mountNS *container.MountNS, mounts []debMount) {
	for _, m := range mounts {
		mountNS.Umount(m.targetPath, 0)
		mountNS.Unlink(m.targetPath)
	}
}

// Install resolves deps for the given package names, then calls InstallResolved.
func Install(ctx context.Context, cont *container.Container, mountNS *container.MountNS, store *objstore.Store, idx *index.Index, arch string, rootfsPath string, names []string) error {
	pkgs, err := Resolve(idx, arch, names)
	if err != nil {
		return fmt.Errorf("resolve: %w", err)
	}
	return InstallResolved(ctx, cont, mountNS, store, rootfsPath, pkgs)
}

// ResetDpkgDatabase empties the dpkg status database, leaving every installed
// file on disk. dpkg records installed state entirely in var/lib/dpkg/status;
// truncating it (and the available catalogue) makes dpkg consider nothing
// installed, so a subsequent install reasons only over the packages it is given,
// not the ones that seeded the base — while the on-disk system state those base
// packages configured (/etc/passwd, the dynamic linker cache, …) is untouched.
// The per-package files under info/ are keyed by status and are rewritten for
// whatever is installed next, so they need no clearing.
func ResetDpkgDatabase(fs container.FsContext, rootfsPath string) error {
	for _, name := range []string{"status", "available"} {
		path := rootfsPath + "/var/lib/dpkg/" + name
		f, err := fs.Open(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return fmt.Errorf("truncate %s: %w", path, err)
		}
		f.Close()
	}
	return nil
}

func execInContainer(ctx context.Context, cont *container.Container, argv []string) error {
	outR, outW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create pipe: %w", err)
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		outR.Close()
		outW.Close()
		return fmt.Errorf("open devnull: %w", err)
	}
	pid, err := cont.Exec(&container.ExecRequest{
		Argv: argv,
		Env:  []string{"HOME=/root", "LANG=C.UTF-8", "DEBIAN_FRONTEND=noninteractive"},
		Cwd:  "/",
		FDs:  []*os.File{devNull, outW, outW},
	})
	devNull.Close()
	outW.Close()
	if err != nil {
		outR.Close()
		return fmt.Errorf("exec %s: %w", argv[0], err)
	}
	output, _ := io.ReadAll(outR)
	outR.Close()
	code, err := cont.Wait(pid)
	if err != nil {
		return fmt.Errorf("wait %s: %w", argv[0], err)
	}
	if code != 0 {
		tail := string(output)
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return fmt.Errorf("%v exited with code %d:\n%s", argv[:2], code, tail)
	}
	return nil
}
