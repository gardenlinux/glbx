package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// buildLocalIndexForBuild creates a package index for the build chroot by
// walking DepArtifacts transitively through their extraDeps and includes. Each
// package reachable along a dep chain (runtime_depends + global depends + same-
// source includes) is loaded into the index from its source-build manifest.
// Source-build manifests are used directly so that sibling Includes — whose
// own validation may not yet have completed when this source build runs — are
// still resolvable.
func (s *DebianPkgBuild) buildLocalIndexForBuild(store *objstore.Store) *index.Index {
	var roots []*debianBinaryPkg
	for _, dep := range s.DepArtifacts {
		if bp, ok := dep.(*debianBinaryPkg); ok {
			roots = append(roots, bp)
		}
	}
	return makeLocalIndex(roots, store)
}

// setupBuildEnv creates the full container stack (BaseExecEnv → UserNS →
// MountNS → Container). Inside the MountNS it mounts a tmpfs, extracts the
// chroot packages via dpkg-deb, copies in the source tree and pinned orig
// archives, and stamps the local-version changelog. The Container is created
// with the tmpfs rootfs as its root.
func (s *DebianPkgBuild) setupBuildEnv(ctx context.Context, store *objstore.Store, pkgs []*index.Package, refs []buildcfg.SourceArchive) (*container.Container, func(), error) {
	l := log.From(ctx, log.Container)

	stubPath := s.StubPath
	if stubPath == "" {
		stubPath = container.StubPath()
	}

	l.Debug("creating namespace stack (stub=%s)", stubPath)
	stack, err := container.NewStack(container.StackConfig{
		Ctx:      ctx,
		StubPath: stubPath,
	})
	if err != nil {
		return nil, nil, err
	}
	mountNS := stack.MountNS

	workPath, err := mountNS.MkTempDir("/tmp", "glbx-build-")
	if err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("mktempdir: %w", err)
	}
	if err := mountNS.Mount("tmpfs", workPath, "tmpfs", 0, "size=4g"); err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("mount tmpfs on %s: %w", workPath, err)
	}

	rootfsPath := workPath + "/rootfs"
	if err := mountNS.Mkdir(rootfsPath, 0755); err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("mkdir %s: %w", rootfsPath, err)
	}

	srcRoot := rootfsPath + "/src"
	if err := mountNS.Mkdir(srcRoot, 0755); err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("mkdir %s: %w", srcRoot, err)
	}
	if err := mountNS.Mount("tmpfs", srcRoot, "tmpfs", 0, "size=16g"); err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("mount tmpfs on %s: %w", srcRoot, err)
	}

	l.Debug("extracting %d build-dep packages", len(pkgs))
	for _, pkg := range pkgs {
		if pkg.SHA256 == "" {
			continue
		}
		hash, err := objstore.NewHash(pkg.SHA256)
		if err != nil {
			continue
		}
		if !store.Blobs.Has(hash) {
			store.EnsureBlob(hash) // pull-through by digest
		}
		if !store.Blobs.Has(hash) {
			continue
		}
		blobPath := store.Blobs.Path(hash)
		if err := container.Run(mountNS, &container.ExecRequest{
			Argv: []string{"dpkg-deb", "-x", blobPath, rootfsPath},
			Env:  []string{"DEBIAN_FRONTEND=noninteractive"},
			Cwd:  "/",
		}); err != nil {
			stack.Close()
			return nil, nil, fmt.Errorf("dpkg-deb -x %s: %w", pkg.Name, err)
		}
	}

	if err := setupDirsInMountNS(mountNS, rootfsPath); err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("setup dirs: %w", err)
	}

	if err := setupLocalRepoInMountNS(mountNS, store, pkgs, rootfsPath); err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("setup local repo: %w", err)
	}

	srcDir := filepath.Join(s.PkgDir, "src")
	srcDst := rootfsPath + "/src/" + s.Name
	l.Debug("copying source tree to %s", srcDst)
	if err := container.Run(mountNS, &container.ExecRequest{
		Argv: []string{"cp", "-a", srcDir, srcDst},
		Cwd:  "/",
	}); err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("copy source: %w", err)
	}

	for _, ref := range refs {
		if strings.HasSuffix(ref.File, ".asc") || strings.HasSuffix(ref.File, ".sig") {
			continue
		}
		store.EnsureBlob(ref.Hash) // pull-through by digest
		blobPath := store.Blobs.Path(ref.Hash)
		tarDst := rootfsPath + "/src/" + ref.File
		if err := mountNS.CreateFile(tarDst, 0644); err != nil {
			l.Warn("create %s: %v", ref.File, err)
			continue
		}
		if err := mountNS.Mount(blobPath, tarDst, "", syscall.MS_BIND|syscall.MS_RDONLY, ""); err != nil {
			l.Warn("bind-mount orig tarball %s: %v", ref.File, err)
			continue
		}
	}

	for i, ref := range refs {
		if strings.HasSuffix(ref.File, ".asc") || strings.HasSuffix(ref.File, ".sig") {
			continue
		}
		tarPath := rootfsPath + "/src/" + ref.File
		if i > 0 && strings.Contains(ref.File, ".orig-") {
			parts := strings.SplitN(ref.File, ".orig-", 2)
			if len(parts) == 2 {
				component := strings.SplitN(parts[1], ".tar", 2)[0]
				componentDir := srcDst + "/" + component
				mountNS.Mkdir(componentDir, 0755)
				if err := container.Run(mountNS, &container.ExecRequest{
					Argv: []string{"tar", "-xf", tarPath, "--strip-components=1", "-C", componentDir},
					Cwd:  "/",
				}); err != nil {
					stack.Close()
					return nil, nil, fmt.Errorf("extract component orig tarball %s: %w", ref.File, err)
				}
				continue
			}
		}
		if err := container.Run(mountNS, &container.ExecRequest{
			Argv: []string{"tar", "-xf", tarPath, "--strip-components=1", "-C", srcDst},
			Cwd:  "/",
		}); err != nil {
			stack.Close()
			return nil, nil, fmt.Errorf("extract orig tarball %s: %w", ref.File, err)
		}
	}

	if err := s.stampLocalChangelog(mountNS, srcDst); err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("stamp gl changelog: %w", err)
	}

	l.Debug("creating container (rootfs on tmpfs)")
	cont, err := container.NewContainer(container.ContainerConfig{
		Ctx:      ctx,
		Parent:   mountNS,
		StubPath: stubPath,
		Rootfs:   rootfsPath,
	})
	if err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("create container: %w", err)
	}
	l.Debug("container ready")

	cleanup := func() {
		cont.Close()
		stack.Close()
	}
	return cont, cleanup, nil
}

// runBuildInContainer registers the unpacked build deps with dpkg, creates the
// build user, sets the synthetic version on debian/changelog, and runs
// dpkg-buildpackage as uid 1000 inside the container.
func (s *DebianPkgBuild) runBuildInContainer(ctx context.Context, cont *container.Container, packages []*index.Package) error {
	l := log.From(ctx, log.Chroot)
	rootEnv := []string{
		"HOME=/root",
		"LANG=C.UTF-8",
		"DEBIAN_FRONTEND=noninteractive",
	}

	var debPaths []string
	for _, pkg := range packages {
		if pkg.SHA256 == "" {
			continue
		}
		debName := fmt.Sprintf("%s_%s_%s.deb", pkg.Name, pkg.Version, pkg.Architecture)
		debPaths = append(debPaths, "/pkgs/"+debName)
	}
	sort.Strings(debPaths)

	l.Info("dpkg --unpack --force-depends (%d packages)", len(debPaths))
	unpackArgv := append([]string{"dpkg", "--unpack", "--force-depends"}, debPaths...)
	if err := execIn(ctx, cont, rootEnv, nil, "/", unpackArgv...); err != nil {
		return fmt.Errorf("dpkg --unpack: %w", err)
	}

	l.Info("dpkg --configure --pending")
	if err := execIn(ctx, cont, rootEnv, nil, "/", "dpkg", "--configure", "--pending"); err != nil {
		return fmt.Errorf("dpkg --configure --pending: %w", err)
	}

	execIn(ctx, cont, rootEnv, nil, "/", "rm", "-f", "/var/lib/dpkg/diversions")
	l.Debug("creating build user (uid=1000)")
	if err := execIn(ctx, cont, rootEnv, nil, "/", "sh", "-c",
		"echo 'dev:x:1000:' >> /etc/group && echo 'dev:x:1000:1000::/home/dev:/bin/bash' >> /etc/passwd && mkdir -p /home/dev && chown 1000:1000 /home/dev"); err != nil {
		return fmt.Errorf("create build user: %w", err)
	}

	execIn(ctx, cont, rootEnv, nil, "/", "chown", "-R", "1000:1000", "/src")

	srcPath := "/src/" + s.Name
	buildCfg, err := buildcfg.LoadBuildYML(s.PkgDir)
	if err != nil {
		return fmt.Errorf("load build.yml: %w", err)
	}

	buildEnv := []string{
		"HOME=/home/dev",
		"LANG=C.UTF-8",
		"DEBIAN_FRONTEND=noninteractive",
	}
	if len(buildCfg.BuildProfiles) > 0 {
		buildEnv = append(buildEnv, "DEB_BUILD_PROFILES="+strings.Join(buildCfg.BuildProfiles, " "))
	}
	if len(buildCfg.BuildOptions) > 0 {
		buildEnv = append(buildEnv, "DEB_BUILD_OPTIONS="+strings.Join(buildCfg.BuildOptions, " "))
	}
	buildEnv = append(buildEnv, buildCfg.ExtraBuildEnv...)

	buildCreds := &container.Credentials{UID: 1000, GID: 1000}

	l.Info("dpkg-buildpackage --no-sign (cwd=%s, uid=1000)", srcPath)
	buildStdout, buildStderr, buildCloseFn := log.NewExecWriters(ctx, log.Chroot)
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		buildCloseFn()
		return err
	}
	pid, err := cont.Exec(&container.ExecRequest{
		Argv:        []string{"dpkg-buildpackage", "--no-sign"},
		Env:         buildEnv,
		Cwd:         srcPath,
		Credentials: buildCreds,
		FDs:         []*os.File{devNull, buildStdout, buildStderr},
	})
	devNull.Close()
	buildStdout.Close()
	buildStderr.Close()
	if err != nil {
		buildCloseFn()
		return fmt.Errorf("exec dpkg-buildpackage: %w", err)
	}
	exitCode, err := cont.Wait(pid)
	buildCloseFn()
	if err != nil {
		return fmt.Errorf("wait dpkg-buildpackage: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("dpkg-buildpackage exited with code %d", exitCode)
	}

	return nil
}

// collectOutputs lists the .deb files produced inside /src/, pipes each into
// the object store, and emits artifact.Output entries (one per .deb plus a
// "control:<name>" stanza blob).
func (s *DebianPkgBuild) collectOutputs(ctx context.Context, cont *container.Container, store *objstore.Store) ([]artifact.Output, error) {
	listing, err := execCapture(ctx, cont, nil, "/src", "find", ".", "-maxdepth", "1", "-name", "*.deb", "-printf", "%f\\n")
	if err != nil {
		return nil, fmt.Errorf("list .deb files: %w", err)
	}

	var debFiles []string
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.HasSuffix(line, ".deb") {
			debFiles = append(debFiles, line)
		}
	}

	if len(debFiles) == 0 {
		return nil, fmt.Errorf("no .deb files found in /src/")
	}

	var outputs []artifact.Output
	for _, debFile := range debFiles {
		containerPath := "/src/" + debFile

		hash, err := pipeFromContainer(ctx, cont, store, containerPath)
		if err != nil {
			return nil, fmt.Errorf("store %s: %w", debFile, err)
		}

		pkgName := extractPkgNameFromDeb(debFile)

		outputs = append(outputs, artifact.Output{
			Name: pkgName + ".deb",
			Hash: hash,
		})

		controlText, err := execCapture(ctx, cont, []string{"DEBIAN_FRONTEND=noninteractive"}, "/", "dpkg-deb", "-f", containerPath)
		if err != nil {
			return nil, fmt.Errorf("dpkg-deb -f %s: %w", debFile, err)
		}

		controlHash, err := store.Blobs.Store(strings.NewReader(controlText))
		if err != nil {
			return nil, fmt.Errorf("store control for %s: %w", pkgName, err)
		}

		outputs = append(outputs, artifact.Output{
			Name: "control:" + pkgName,
			Hash: controlHash,
		})
	}

	return outputs, nil
}

func extractPkgNameFromDeb(debPath string) string {
	base := filepath.Base(debPath)
	parts := strings.SplitN(base, "_", 2)
	if len(parts) >= 1 {
		return parts[0]
	}
	return ""
}
