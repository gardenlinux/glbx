package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/install"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// Rootfs represents a root filesystem artifact assembled via the three-layer
// overlay model:
//   - Layer 0: locally-built packages (raw extracted)
//   - Layer 1: lockfile packages for Debian infrastructure (raw extracted,
//     filtered to exclude anything already in Layer 0)
//   - Layer 2: upper dir capturing mutations from `dpkg --unpack` /
//     `dpkg --configure --pending` (postinst scripts, ldconfig,
//     update-alternatives, etc.)
//
// Final output = Layer 0 + Layer 2 (Layer 1 is discarded).
type Rootfs struct {
	Name       string
	Arch       string
	DirectDeps []*debianBinaryPkg
	store      *objstore.Store
	identity   objstore.Hash
	baseDir    string
	stubPath   string
}

// RootfsConfig holds the parameters for constructing a new Rootfs artifact.
type RootfsConfig struct {
	Name     string
	Arch     string
	Store    *objstore.Store
	PkgSet   *PackageSet
	BaseDir  string
	StubPath string
}

// NewRootfs creates a new Rootfs artifact. It reads rootfs.yml from BaseDir
// and resolves src:pkg entries to binary package artifacts via the PackageSet.
func NewRootfs(cfg RootfsConfig) (*Rootfs, error) {
	entries := buildcfg.LoadRootfsYML(cfg.BaseDir)
	if len(entries) == 0 {
		return nil, fmt.Errorf("no rootfs.yml found (or empty) in %s", cfg.BaseDir)
	}

	var deps []*debianBinaryPkg
	for _, spec := range entries {
		parts := strings.SplitN(spec, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("rootfs.yml: %q must be in src:pkg format", spec)
		}
		bp, err := cfg.PkgSet.Binary(parts[0], parts[1])
		if err != nil {
			return nil, fmt.Errorf("rootfs.yml: %w", err)
		}
		deps = append(deps, bp)
	}

	return &Rootfs{
		Name:       cfg.Name,
		Arch:       cfg.Arch,
		DirectDeps: deps,
		store:      cfg.Store,
		baseDir:    cfg.BaseDir,
		stubPath:   cfg.StubPath,
	}, nil
}

func (r *Rootfs) Key() string {
	return fmt.Sprintf("rootfs:%s:%s", r.Name, r.Arch)
}

// newRootfsDirect creates a Rootfs with explicit deps (for testing).
func newRootfsDirect(name, arch string, deps []*debianBinaryPkg, store *objstore.Store) *Rootfs {
	return &Rootfs{
		Name:       name,
		Arch:       arch,
		DirectDeps: deps,
		store:      store,
	}
}

func (r *Rootfs) String() string {
	return fmt.Sprintf("rootfs:%s", r.Name)
}

func (r *Rootfs) Depends() []artifact.Artifact {
	deps := make([]artifact.Artifact, len(r.DirectDeps))
	for i, bp := range r.DirectDeps {
		deps[i] = bp
	}
	return deps
}

// Includes returns no closure-only edges at the rootfs level — the rootfs
// already walks the full closure of its DirectDeps via Inputs() and
// allBinaryDeps(), pulling in sibling Includes transitively.
func (r *Rootfs) Includes() []artifact.Artifact { return nil }

func (r *Rootfs) OutputRefs(store *objstore.Store) (objstore.Hash, []objstore.Hash, error) {
	return artifact.ResolveOutputRefs(r, store)
}

// Inputs references the .deb and control outputs from each binary dependency.
func (r *Rootfs) Inputs() []artifact.Input {
	var inputs []artifact.Input
	for _, bp := range r.allBinaryDeps() {
		inputs = append(inputs, artifact.Input{
			Source: bp,
			Name:   bp.name + ".deb",
		})
		inputs = append(inputs, artifact.Input{
			Source: bp,
			Name:   "control:" + bp.name,
		})
	}
	return inputs
}

// allBinaryDeps walks the transitive closure of binary package dependencies
// (DirectDeps + their extraDeps + their sibling includes, recursively).
func (r *Rootfs) allBinaryDeps() []*debianBinaryPkg {
	return walkBinaries(r.DirectDeps)
}

func (r *Rootfs) Identity() (objstore.Hash, error) {
	if !r.identity.IsZero() {
		return r.identity, nil
	}

	parts := []string{identityScheme, "rootfs", r.Name, r.Arch}

	// Fold the content hash of every pinned image-tooling file.
	if tools, err := r.loadImageToolingPins(); err == nil {
		for _, t := range tools {
			for _, f := range t.Files {
				parts = append(parts, f.Hash.String())
			}
		}
	}

	for _, bp := range r.allBinaryDeps() {
		id, err := bp.Identity()
		if err != nil {
			return objstore.Hash{}, fmt.Errorf("get binary package identity for %s: %w", bp.name, err)
		}
		parts = append(parts, id.String())
	}

	hash := objstore.ConcatHash(parts...)
	r.identity = hash
	return hash, nil
}

// loadImageToolingPins reads the explicit rootfs-deps.yml pins.
func (r *Rootfs) loadImageToolingPins() ([]buildcfg.PinnedTool, error) {
	return buildcfg.LoadBuildDeps(filepath.Join(r.baseDir, "rootfs-deps.yml"))
}

// loadLockfileIndex builds an index of the pinned image-configuration tooling
// (Layer 1) for the target architecture from the explicit rootfs-deps.yml pins.
func (r *Rootfs) loadLockfileIndex() (*index.Index, error) {
	tools, err := r.loadImageToolingPins()
	if err != nil {
		return nil, err
	}
	idx := index.New()
	for _, tool := range tools {
		for _, f := range tool.Files {
			if f.Arch != r.Arch && f.Arch != "all" {
				continue
			}
			stanza := map[string]string{
				"package":      tool.Name,
				"version":      tool.Version,
				"architecture": f.Arch,
				"sha256":       f.Hash.String(),
			}
			pkg, err := index.ParsePackageFromStanza(stanza)
			if err != nil {
				return nil, fmt.Errorf("image tool %s: %w", tool.Name, err)
			}
			if idx.Get(pkg.Name) == nil {
				idx.Add(pkg)
			}
			break
		}
	}
	return idx, nil
}

// rootfsLocalPkg holds a local package's name and .deb blob hash.
type rootfsLocalPkg struct {
	name    string
	debHash objstore.Hash
}

// buildLocalIndex constructs a package index from the source-build manifests of
// every binary in the build closure. Used to drive install.Resolve so we can
// narrow Layer 0 + Layer 2 to the actual runtime install closure of DirectDeps,
// dropping -dev / -static siblings that the build closure pulls in.
func (r *Rootfs) buildLocalIndex(store *objstore.Store) *index.Index {
	return makeLocalIndex(r.DirectDeps, store)
}

// rootfsPaths captures the layout of the in-MountNS workspace used during
// rootfs assembly.
type rootfsPaths struct {
	work       string
	layer0     string
	layer1     string
	layer2     string
	layer2work string
	merged     string
	final      string
}

// Build assembles the rootfs using the three-layer overlay model.
func (r *Rootfs) Build(ctx artifact.BuildContext) ([]artifact.Output, error) {
	store := ctx.Store
	l := log.From(ctx.Ctx, log.Rootfs)
	l.Info("building rootfs: %s", r.Name)

	localPkgs, err := r.collectLocalPkgs(ctx.Inputs)
	if err != nil {
		return nil, err
	}
	l.Info("%d local packages available", len(localPkgs))

	installPkgs, err := r.narrowInstallSet(localPkgs, store)
	if err != nil {
		return nil, err
	}
	l.Info("%d packages in runtime install set (Layer 0 + Layer 2)", len(installPkgs))

	installNames := make(map[string]bool, len(installPkgs))
	for _, lp := range installPkgs {
		installNames[lp.name] = true
	}
	layer1Pkgs, err := r.loadLayer1Pkgs(installNames)
	if err != nil {
		return nil, err
	}
	l.Info("%d lockfile packages for Layer 1 (after filtering)", len(layer1Pkgs))

	stubPath := r.stubPath
	if stubPath == "" {
		stubPath = container.StubPath()
	}
	stack, err := container.NewStack(container.StackConfig{
		Ctx:      ctx.Ctx,
		StubPath: stubPath,
	})
	if err != nil {
		return nil, err
	}
	defer stack.Close()
	mountNS := stack.MountNS

	paths, err := r.createWorkspace(mountNS)
	if err != nil {
		return nil, err
	}

	if err := r.extractLayers(ctx.Ctx, mountNS, store, installPkgs, layer1Pkgs, paths); err != nil {
		return nil, err
	}

	if err := r.runDpkg(ctx.Ctx, mountNS, store, installPkgs, ctx.Inputs, paths, stubPath); err != nil {
		return nil, err
	}

	return r.packFinal(ctx.Ctx, mountNS, store, paths)
}

// collectLocalPkgs separates the .deb inputs from ctx.Inputs into a slice,
// returning an error if no .debs are present.
func (r *Rootfs) collectLocalPkgs(inputs map[string]objstore.Hash) ([]rootfsLocalPkg, error) {
	var localPkgs []rootfsLocalPkg
	for name, hash := range inputs {
		if strings.HasSuffix(name, ".deb") {
			pkgName := strings.TrimSuffix(name, ".deb")
			localPkgs = append(localPkgs, rootfsLocalPkg{name: pkgName, debHash: hash})
		}
	}
	if len(localPkgs) == 0 {
		return nil, fmt.Errorf("rootfs %s: no .deb packages to install", r.Name)
	}
	return localPkgs, nil
}

// narrowInstallSet runs install.Resolve against the local index merged with the
// image-configuration tooling to drop build-only siblings (-dev, -static) from
// the runtime install closure while still resolving externals (e.g. libc6) that
// Layer 1 supplies.
func (r *Rootfs) narrowInstallSet(localPkgs []rootfsLocalPkg, store *objstore.Store) ([]rootfsLocalPkg, error) {
	localIdx := r.buildLocalIndex(store)
	resolveIdx := localIdx
	if toolingIdx, err := r.loadLockfileIndex(); err == nil {
		resolveIdx = localIdx.Merge(toolingIdx)
	}
	directNames := make([]string, len(r.DirectDeps))
	for i, bp := range r.DirectDeps {
		directNames[i] = bp.name
	}
	resolved, err := install.Resolve(resolveIdx, r.Arch, directNames)
	if err != nil {
		return nil, fmt.Errorf("resolve rootfs install set: %w", err)
	}
	resolvedNames := make(map[string]bool, len(resolved))
	for _, pkg := range resolved {
		resolvedNames[pkg.Name] = true
	}
	// Keep only locally-built packages in the install set; Layer 1 externals
	// stay in Layer 1 and are discarded from the final image.
	var installPkgs []rootfsLocalPkg
	for _, lp := range localPkgs {
		if resolvedNames[lp.name] {
			installPkgs = append(installPkgs, lp)
		}
	}
	return installPkgs, nil
}

// loadLayer1Pkgs returns the lockfile packages minus those already provided by
// Layer 0 (matched by name). The filter set must be the names actually
// extracted into Layer 0 (the narrowed install set), not the full build
// closure: a package can appear in the build closure as a sibling-include
// (e.g. libc-bin pulled in by libc6's runtime_depends for source-build
// install-check coupling) without being part of the rootfs's runtime install
// set, in which case the lockfile-mirror copy is still what belongs in Layer 1.
func (r *Rootfs) loadLayer1Pkgs(installNames map[string]bool) ([]*index.Package, error) {
	lockfileIdx, err := r.loadLockfileIndex()
	if err != nil {
		return nil, fmt.Errorf("load rootfs lockfile: %w", err)
	}
	var layer1Pkgs []*index.Package
	for _, pkg := range lockfileIdx.All() {
		if !installNames[pkg.Name] {
			layer1Pkgs = append(layer1Pkgs, pkg)
		}
	}
	return layer1Pkgs, nil
}

// createWorkspace mounts a tmpfs workspace and creates the layer dirs.
func (r *Rootfs) createWorkspace(mountNS *container.MountNS) (*rootfsPaths, error) {
	work, err := mountNS.MkTempDir("/tmp", "glbx-rootfs-")
	if err != nil {
		return nil, fmt.Errorf("mktempdir: %w", err)
	}
	if err := mountNS.Mount("tmpfs", work, "tmpfs", 0, "size=4g"); err != nil {
		return nil, fmt.Errorf("mount tmpfs: %w", err)
	}
	p := &rootfsPaths{
		work:       work,
		layer0:     work + "/layer0",
		layer1:     work + "/layer1",
		layer2:     work + "/layer2",
		layer2work: work + "/layer2.work",
		merged:     work + "/merged",
		final:      work + "/final",
	}
	for _, d := range []string{p.layer0, p.layer1, p.layer2, p.layer2work, p.merged, p.final} {
		if err := mountNS.Mkdir(d, 0755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return p, nil
}

// extractLayers populates layer0 (local install set) and layer1 (filtered
// lockfile packages) by raw-extracting each .deb. Also creates the proc/sys/dev
// mount point dirs in layer0 — base-files supplies the rest of the layout.
func (r *Rootfs) extractLayers(ctx context.Context, mountNS *container.MountNS, store *objstore.Store, installPkgs []rootfsLocalPkg, layer1Pkgs []*index.Package, paths *rootfsPaths) error {
	l := log.From(ctx, log.Rootfs)

	l.Info("extracting Layer 0 (local packages)")
	for _, lp := range installPkgs {
		store.EnsureBlob(lp.debHash) // pull-through by digest
		if !store.Blobs.Has(lp.debHash) {
			return fmt.Errorf("local deb blob %s (%s) not in store", lp.debHash, lp.name)
		}
		blobPath := store.Blobs.Path(lp.debHash)
		if err := container.Run(mountNS, &container.ExecRequest{
			Argv: []string{"dpkg-deb", "-x", blobPath, paths.layer0},
			Env:  []string{"DEBIAN_FRONTEND=noninteractive"},
			Cwd:  "/",
		}); err != nil {
			return fmt.Errorf("extract layer0 %s: %w", lp.name, err)
		}
	}

	l.Info("extracting Layer 1 (lockfile packages)")
	for _, pkg := range layer1Pkgs {
		if pkg.SHA256 == "" {
			continue
		}
		hash, err := objstore.NewHash(pkg.SHA256)
		if err != nil {
			continue
		}
		store.EnsureBlob(hash) // pull-through by digest
		if !store.Blobs.Has(hash) {
			continue
		}
		blobPath := store.Blobs.Path(hash)
		if err := container.Run(mountNS, &container.ExecRequest{
			Argv: []string{"dpkg-deb", "-x", blobPath, paths.layer1},
			Env:  []string{"DEBIAN_FRONTEND=noninteractive"},
			Cwd:  "/",
		}); err != nil {
			l.Warn("failed to extract %s: %v", pkg.Name, err)
		}
	}

	for _, d := range []string{"proc", "sys", "dev"} {
		mountNS.Mkdir(paths.layer0+"/"+d, 0755)
	}
	return nil
}

// runDpkg mounts the build overlay (layer0 + layer1 lower, layer2 upper),
// bind-mounts local .debs into /pkgs, runs `dpkg --unpack` and
// `dpkg --configure --pending` inside the resulting container, then unmounts
// the overlay so packFinal can re-mount layer0 + layer2 read-only.
func (r *Rootfs) runDpkg(ctx context.Context, mountNS *container.MountNS, store *objstore.Store, installPkgs []rootfsLocalPkg, inputs map[string]objstore.Hash, paths *rootfsPaths, stubPath string) error {
	l := log.From(ctx, log.Rootfs)

	overlayOpts := fmt.Sprintf("lowerdir=%s:%s,upperdir=%s,workdir=%s", paths.layer0, paths.layer1, paths.layer2, paths.layer2work)
	if err := mountNS.Mount("overlay", paths.merged, "overlay", 0, overlayOpts); err != nil {
		return fmt.Errorf("mount overlay: %w", err)
	}
	l.Debug("overlay mounted at %s", paths.merged)

	if err := r.setupRootfsRepo(mountNS, store, installPkgs, inputs, paths.merged); err != nil {
		return fmt.Errorf("setup rootfs repo: %w", err)
	}

	cont, err := container.NewContainer(container.ContainerConfig{
		Ctx:      ctx,
		Parent:   mountNS,
		StubPath: stubPath,
		Rootfs:   paths.merged,
	})
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}

	rootEnv := []string{
		"HOME=/root",
		"LANG=C.UTF-8",
		"DEBIAN_FRONTEND=noninteractive",
	}

	l.Info("dpkg --unpack --force-depends (%d local packages)", len(installPkgs))
	if err := execIn(ctx, cont, rootEnv, nil, "/", "sh", "-c",
		"dpkg --unpack --force-depends /pkgs/*.deb"); err != nil {
		cont.Close()
		return fmt.Errorf("dpkg --unpack: %w", err)
	}

	l.Info("dpkg --configure --pending")
	if err := execIn(ctx, cont, rootEnv, nil, "/", "dpkg", "--configure", "--pending"); err != nil {
		cont.Close()
		return fmt.Errorf("dpkg --configure --pending: %w", err)
	}

	if err := cont.Symlink("/bin/bash", "/bin/sh"); err != nil && !os.IsExist(err) {
		cont.Close()
		return fmt.Errorf("create /bin/sh symlink: %w", err)
	}

	cont.Close()

	if err := mountNS.Umount(paths.merged, syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("umount overlay: %w", err)
	}
	return nil
}

// packFinal mounts the final read-only overlay (layer2 on top of layer0) and
// packs it as a deterministic tar.gz blob in the object store.
func (r *Rootfs) packFinal(ctx context.Context, mountNS *container.MountNS, store *objstore.Store, paths *rootfsPaths) ([]artifact.Output, error) {
	l := log.From(ctx, log.Rootfs)

	finalOpts := fmt.Sprintf("lowerdir=%s:%s", paths.layer2, paths.layer0)
	if err := mountNS.Mount("overlay", paths.final, "overlay", 0, finalOpts); err != nil {
		return nil, fmt.Errorf("mount final overlay: %w", err)
	}
	l.Debug("final overlay (layer0 + layer2) mounted at %s", paths.final)

	l.Info("packing as tar.gz")
	tarHash, err := packRootfsTarFromMountNS(ctx, mountNS, paths.final, store)
	if err != nil {
		return nil, fmt.Errorf("pack rootfs tar: %w", err)
	}

	return []artifact.Output{
		{Name: "rootfs.tar.gz", Hash: tarHash},
	}, nil
}

// setupRootfsRepo bind-mounts each locally-built .deb into rootfs/pkgs/ at its
// canonical <name>_<ver>_<arch>.deb path so the in-container `dpkg --unpack
// /pkgs/*.deb` step has every package available. No apt repo metadata is
// written — installation is dpkg-only.
func (r *Rootfs) setupRootfsRepo(mountNS *container.MountNS, store *objstore.Store, localPkgs []rootfsLocalPkg, inputs map[string]objstore.Hash, rootfs string) error {
	if err := mountNS.Mkdir(rootfs+"/pkgs", 0755); err != nil {
		return fmt.Errorf("mkdir /pkgs: %w", err)
	}

	for _, lp := range localPkgs {
		store.EnsureBlob(lp.debHash) // pull-through by digest
		if !store.Blobs.Has(lp.debHash) {
			continue
		}
		blobPath := store.Blobs.Path(lp.debHash)

		controlKey := "control:" + lp.name
		controlHash, hasControl := inputs[controlKey]

		var pkg *index.Package
		if hasControl {
			store.EnsureBlob(controlHash) // pull-through by digest
		}
		if hasControl && store.Blobs.Has(controlHash) {
			f, err := os.Open(store.Blobs.Path(controlHash))
			if err == nil {
				idx, err := index.Load(f)
				f.Close()
				if err == nil && idx.Len() > 0 {
					pkg = idx.All()[0]
				}
			}
		}

		var debName string
		if pkg != nil {
			debName = fmt.Sprintf("%s_%s_%s.deb", pkg.Name, pkg.Version, pkg.Architecture)
		} else {
			debName = lp.name + ".deb"
		}

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

// packRootfsTarFromMountNS packs the rootfs directory (inside the MountNS) as
// a tar.gz and stores it in the object store.
func packRootfsTarFromMountNS(ctx context.Context, mountNS *container.MountNS, rootfsPath string, store *objstore.Store) (objstore.Hash, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("create pipe: %w", err)
	}

	devNull, err := os.Open("/dev/null")
	if err != nil {
		pr.Close()
		pw.Close()
		return objstore.Hash{}, fmt.Errorf("open /dev/null: %w", err)
	}
	defer devNull.Close()

	_, stderrFD, closeFn := log.NewExecWriters(ctx, log.Rootfs)

	pid, err := mountNS.Exec(&container.ExecRequest{
		Argv: []string{"tar", "-czf", "-", "--mtime=@0", "--sort=name",
			"--numeric-owner", "-C", rootfsPath, "."},
		Cwd: "/",
		FDs: []*os.File{devNull, pw, stderrFD},
	})
	pw.Close()
	stderrFD.Close()
	if err != nil {
		closeFn()
		pr.Close()
		return objstore.Hash{}, fmt.Errorf("exec tar: %w", err)
	}

	hash, err := store.Blobs.Store(pr)
	pr.Close()
	if err != nil {
		mountNS.Wait(pid)
		closeFn()
		return objstore.Hash{}, fmt.Errorf("store tar blob: %w", err)
	}

	exitCode, err := mountNS.Wait(pid)
	closeFn()
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("wait tar: %w", err)
	}
	if exitCode != 0 {
		return objstore.Hash{}, fmt.Errorf("tar exited with code %d", exitCode)
	}

	return hash, nil
}
