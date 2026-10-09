package build

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/debian/deb822"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/install"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// installCheck verifies the binary installs cleanly when its runtime closure is
// drawn from locally built packages (plus its declared lockfile_deps). It
// bootstraps a base system from the pinned build tooling, then resolves and
// installs the binary and its local closure with dpkg. A missing local
// dependency fails resolution; a dpkg problem fails the install — both are
// correct signals that the local build universe is incomplete.
func (b *debianBinaryPkg) installCheck(ctx context.Context, store *objstore.Store, debHash, controlHash objstore.Hash) error {
	l := log.From(ctx, log.InstallCheck)

	// Base system for the bootstrap: the pinned build-tooling set.
	bootstrapIndex, err := b.sourceBuild.loadLockfileIndex()
	if err != nil {
		return fmt.Errorf("load build-deps for install check: %w", err)
	}

	localIndex := b.buildLocalIndex(store)
	if testPkg := b.buildTestPackageEntry(store, controlHash, debHash); testPkg != nil {
		localIndex.Add(testPkg)
	}
	l.Info("%s: %d local packages for install check", b.name, localIndex.Len())

	// Per-binary lockfile_deps: pull the named externals from the pinned set
	// into the local index so resolution of the test package can see them. A
	// name not present in the pinned tooling is a tolerance that does not apply
	// to the current archive — skip it rather than failing.
	if names := b.lockfileDepNames(); len(names) > 0 {
		var present []string
		for _, n := range names {
			if bootstrapIndex.Get(n) != nil {
				present = append(present, n)
			} else {
				l.Debug("%s: lockfile_dep %q not in pinned tooling; skipping", b.name, n)
			}
		}
		if len(present) > 0 {
			extra, err := install.Resolve(bootstrapIndex, b.sourceBuild.Arch, present)
			if err != nil {
				return fmt.Errorf("resolve lockfile_deps for %s: %w", b.name, err)
			}
			for _, pkg := range extra {
				if localIndex.Get(pkg.Name) == nil {
					localIndex.Add(pkg)
				}
			}
			l.Info("%s: added %d lockfile_deps packages", b.name, len(extra))
		}
	}

	resolved, err := install.Resolve(localIndex, b.sourceBuild.Arch, []string{b.name})
	if err != nil {
		return fmt.Errorf("resolve %s from local index: %w", b.name, err)
	}
	l.Info("%s: resolved %d packages for install", b.name, len(resolved))

	stubPath := b.sourceBuild.StubPath
	if stubPath == "" {
		stubPath = container.StubPath()
	}
	stack, err := container.NewStack(container.StackConfig{Ctx: ctx, StubPath: stubPath})
	if err != nil {
		return err
	}
	defer stack.Close()
	mountNS := stack.MountNS
	mountNS.Mkdir("/tmp", 01777)

	// Bootstrap a working base from the pinned tooling's essential set: extract
	// and fully dpkg-configure it, so the chroot has the system state a package's
	// maintainer scripts assume (a populated /etc/passwd from base-passwd, a
	// configured libc, ldconfig run, …). This base is drawn from the pinned
	// tooling, which may be a different point in the archive's history than our
	// locally built libraries.
	cont, rootfsPath, contCleanup, err := install.Bootstrap(ctx, mountNS, store, bootstrapIndex, b.sourceBuild.Arch, stubPath)
	if err != nil {
		return fmt.Errorf("bootstrap for install check: %w", err)
	}
	defer func() {
		if contCleanup != nil {
			contCleanup()
		}
	}()

	// Wipe the dpkg database, keeping the base's files on disk. dpkg now has no
	// record of the base packages, so installing our local closure on top does
	// not re-configure them or check their dependencies — dpkg reasons only over
	// the packages we build. The base's filesystem state (/etc/passwd, the
	// configured libc, …) remains to satisfy maintainer scripts, while a base
	// package whose version is coupled to a different libc than ours can no
	// longer break the install by being reconfigured against the wrong library.
	if err := install.ResetDpkgDatabase(mountNS, rootfsPath); err != nil {
		return fmt.Errorf("reset dpkg database for install check: %w", err)
	}

	// Install the binary's local closure on top of the wiped base. The locality
	// check already proved every runtime dependency is locally built (or an
	// allowed external); this step proves the binary physically unpacks and
	// configures with dpkg against a database that knows only our packages.
	if err := install.InstallResolved(ctx, cont, mountNS, store, rootfsPath, resolved); err != nil {
		return fmt.Errorf("install check failed for %s: %w", b.name, err)
	}

	l.Info("%s: install check passed", b.name)
	return nil
}

// buildTestPackageEntry constructs the index.Package for the binary under test
// from its control blob and .deb hash.
func (b *debianBinaryPkg) buildTestPackageEntry(store *objstore.Store, controlHash, debHash objstore.Hash) *index.Package {
	controlReader, err := store.Blobs.Open(controlHash)
	if err != nil {
		return nil
	}
	stanza, err := deb822.NewReader(controlReader).Next()
	controlReader.Close()
	if err != nil {
		return nil
	}
	pkg, err := index.ParsePackageFromStanza(stanza)
	if err != nil {
		return nil
	}
	if !debHash.IsZero() {
		pkg.SHA256 = debHash.String()
		pkg.Stanza["sha256"] = debHash.String()
		if p, err := store.Blobs.Path(debHash); err == nil {
			if info, err := os.Stat(p); err == nil {
				pkg.Size = info.Size()
				pkg.Stanza["size"] = strconv.FormatInt(info.Size(), 10)
			}
		}
	}
	return pkg
}

// buildLocalIndex builds a local-only index from the source-build manifests of
// every binary reachable from this one via extraDeps and includes.
func (b *debianBinaryPkg) buildLocalIndex(store *objstore.Store) *index.Index {
	b.resolveExtraDeps()
	roots := append([]*debianBinaryPkg{}, b.extraDeps...)
	roots = append(roots, b.includes...)
	return makeLocalIndex(roots, store)
}
