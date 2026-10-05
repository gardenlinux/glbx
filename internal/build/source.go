package build

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/dirhash"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/stream"
)

// identityScheme labels the identity-fold construction. It is the first element
// of every artifact identity, so a change to the scheme changes every identity.
const identityScheme = "glbx-identity-v1"

// localVersionSuffix separates a package's base version from the content-derived
// local-build suffix (base+gl~<srcHash[:8]>).
const localVersionTag = "+gl~"

// binaryResolver resolves a "<source>:<binary>" reference to the binary
// artifact that produces it. The working-tree package set implements it; a
// source build with no resolver (constructed with explicit deps) does not need
// one.
type binaryResolver interface {
	Binary(srcName, binName string) (*debianBinaryPkg, error)
	ProvidesMap() map[string][]string
}

// LoadBinaryPkg returns the index.Package for the named binary, read from the
// source build's manifest in the store, or nil if the source build is not yet
// built or the binary is absent from the manifest. The source build is always
// a Depends of any binary, so its manifest is present whenever a binary builds.
func (s *DebianPkgBuild) LoadBinaryPkg(binaryName string, store *objstore.Store) *index.Package {
	srcID, err := s.Identity()
	if err != nil {
		return nil
	}
	manifestHash, err := store.MapGet(srcID)
	if err != nil {
		return nil
	}
	return parsePackageFromManifest(store, manifestHash, binaryName)
}

// DebianPkgBuild is the source-build artifact: it compiles one source package
// into its binary .debs inside the sandbox.
type DebianPkgBuild struct {
	Name         string
	PkgDir       string
	Arch         string
	DepArtifacts []artifact.Artifact
	StubPath     string
	store        *objstore.Store
	identity     objstore.Hash
	binaries     map[string]*debianBinaryPkg
	pkgSet       binaryResolver
	depsResolved bool
}

type DebianPkgBuildConfig struct {
	Name         string
	PkgDir       string
	Arch         string
	DepArtifacts []artifact.Artifact
	Store        *objstore.Store
	StubPath     string
}

func NewDebianPkgBuild(cfg DebianPkgBuildConfig) *DebianPkgBuild {
	pkgDir := cfg.PkgDir
	if abs, err := filepath.Abs(pkgDir); err == nil {
		pkgDir = abs
	}
	stubPath := cfg.StubPath
	if stubPath != "" {
		if abs, err := filepath.Abs(stubPath); err == nil {
			stubPath = abs
		}
	}
	return &DebianPkgBuild{
		Name:         cfg.Name,
		PkgDir:       pkgDir,
		Arch:         cfg.Arch,
		DepArtifacts: cfg.DepArtifacts,
		StubPath:     stubPath,
		store:        cfg.Store,
		binaries:     make(map[string]*debianBinaryPkg),
	}
}

// Binary returns, creating if needed, the binary-package artifact for the named
// binary produced by this source build.
func (d *DebianPkgBuild) Binary(name string) *debianBinaryPkg {
	if bp, ok := d.binaries[name]; ok {
		return bp
	}
	bp := &debianBinaryPkg{
		name:        name,
		sourceBuild: d,
		store:       d.store,
	}
	d.binaries[name] = bp
	return bp
}

func (s *DebianPkgBuild) Key() string {
	return fmt.Sprintf("debian-pkg-build:%s:%s", s.Name, s.Arch)
}

func (s *DebianPkgBuild) String() string {
	return fmt.Sprintf("debian-pkg-build:%s", s.Name)
}

func (s *DebianPkgBuild) Depends() []artifact.Artifact {
	s.resolveDeps()
	return s.DepArtifacts
}

// Includes returns no closure-only edges — source builds have none.
func (s *DebianPkgBuild) Includes() []artifact.Artifact { return nil }

func (s *DebianPkgBuild) OutputRefs(store *objstore.Store) (objstore.Hash, []objstore.Hash, error) {
	return artifact.ResolveOutputRefs(s, store)
}

func (s *DebianPkgBuild) resolveDeps() {
	if s.depsResolved || s.pkgSet == nil {
		return
	}
	s.depsResolved = true

	buildYML, err := buildcfg.LoadBuildYML(s.PkgDir)
	if err != nil {
		return
	}
	for _, depSpec := range buildYML.BuildDepends {
		src, bin, ok := strings.Cut(depSpec, ":")
		if !ok {
			continue
		}
		bp, err := s.pkgSet.Binary(src, bin)
		if err != nil {
			continue
		}
		s.DepArtifacts = append(s.DepArtifacts, bp)
	}
}

func (s *DebianPkgBuild) Inputs() []artifact.Input {
	var inputs []artifact.Input
	for _, dep := range s.DepArtifacts {
		bp, ok := dep.(*debianBinaryPkg)
		if !ok {
			continue
		}
		inputs = append(inputs, artifact.Input{Source: bp, Name: bp.name + ".deb"})
		inputs = append(inputs, artifact.Input{Source: bp, Name: "control:" + bp.name})
	}
	return inputs
}

func (s *DebianPkgBuild) Identity() (objstore.Hash, error) {
	if !s.identity.IsZero() {
		return s.identity, nil
	}

	dirHash, err := dirhash.HashDirectory(s.PkgDir)
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("hash package directory: %w", err)
	}

	parts := []string{identityScheme, dirHash, s.Arch}
	for _, dep := range s.DepArtifacts {
		id, err := dep.Identity()
		if err != nil {
			return objstore.Hash{}, fmt.Errorf("get dep identity: %w", err)
		}
		parts = append(parts, id.String())
	}

	hash := objstore.ConcatHash(parts...)
	s.identity = hash
	return hash, nil
}

func (s *DebianPkgBuild) Build(ctx artifact.BuildContext) ([]artifact.Output, error) {
	store := ctx.Store
	l := log.From(ctx.Ctx, log.Build)
	l.Info("building source package: %s", s.Name)

	lockfileIndex, err := s.loadLockfileIndex()
	if err != nil {
		return nil, fmt.Errorf("load build-deps: %w", err)
	}

	// The chroot gets the pinned build tooling plus every locally-built
	// dependency binary layered on top.
	chrootPkgs := lockfileIndex.All()
	for _, pkg := range s.buildLocalIndexForBuild(store).All() {
		chrootPkgs = append(chrootPkgs, pkg)
	}
	l.Info("%d chroot packages (%d pinned tooling)", len(chrootPkgs), lockfileIndex.Len())

	refs, err := buildcfg.LoadSourcesYML(s.PkgDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load sources.yml: %w", err)
	}

	cont, cleanup, err := s.setupBuildEnv(ctx.Ctx, store, chrootPkgs, refs)
	if err != nil {
		return nil, fmt.Errorf("setup build env: %w", err)
	}
	defer cleanup()

	if err := s.runBuildInContainer(ctx.Ctx, cont, chrootPkgs); err != nil {
		return nil, fmt.Errorf("run build: %w", err)
	}

	outputs, err := s.collectOutputs(ctx.Ctx, cont, store)
	if err != nil {
		return nil, fmt.Errorf("collect outputs: %w", err)
	}
	return outputs, nil
}

// loadLockfileIndex reads the explicit build-deps.yml pins and builds an index
// of the pinned build-tooling packages for the target architecture. The pins
// are already a resolved closure, so each becomes one package entry carrying
// its name, exact version, architecture, and content hash — the chroot installs
// exactly this set, with no further resolution.
func (s *DebianPkgBuild) loadLockfileIndex() (*index.Index, error) {
	tools, err := buildcfg.LoadBuildDeps(filepath.Join(s.PkgDir, "build-deps.yml"))
	if err != nil {
		return nil, err
	}
	idx := index.New()
	for _, tool := range tools {
		for _, f := range tool.Files {
			if f.Arch != s.Arch && f.Arch != "all" {
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
				return nil, fmt.Errorf("tool %s: %w", tool.Name, err)
			}
			if idx.Get(pkg.Name) == nil {
				idx.Add(pkg)
			}
			break
		}
	}
	return idx, nil
}

func (s *DebianPkgBuild) computeVersion() (string, error) {
	_, baseVersion, _, err := s.parseChangelogTopEntry()
	if err != nil {
		return "0.0-0" + localVersionTag + "00000000", nil
	}

	srcDir := filepath.Join(s.PkgDir, "src")
	srcHash, err := dirhash.HashDirectory(srcDir)
	if err != nil {
		srcHash = "0000000000000000000000000000000000000000000000000000000000000000"
	}
	return baseVersion + localVersionTag + srcHash[:8], nil
}

// parseChangelogTopEntry reads the first debian/changelog entry and returns the
// source name, the base version in parens, and the trailer date. The trailer
// date is what dpkg-buildpackage uses for SOURCE_DATE_EPOCH, so it is mirrored
// onto the synthetic entry to keep the produced .deb reproducible.
func (s *DebianPkgBuild) parseChangelogTopEntry() (name, baseVersion, date string, err error) {
	data, err := os.ReadFile(filepath.Join(s.PkgDir, "src", "debian", "changelog"))
	if err != nil {
		return "", "", "", err
	}
	return parseChangelogTopEntryBytes(data)
}

func parseChangelogTopEntryBytes(data []byte) (name, baseVersion, date string, err error) {
	s := string(data)
	header, rest, _ := strings.Cut(s, "\n")

	open := strings.Index(header, "(")
	closeParen := strings.Index(header, ")")
	if open < 0 || closeParen < 0 || closeParen <= open {
		return "", "", "", fmt.Errorf("malformed changelog header: %q", header)
	}
	name = strings.TrimSpace(header[:open])
	if name == "" {
		return "", "", "", fmt.Errorf("empty source name in changelog header: %q", header)
	}
	baseVersion = header[open+1 : closeParen]

	for _, line := range strings.Split(rest, "\n") {
		if !strings.HasPrefix(line, " -- ") {
			continue
		}
		idx := strings.LastIndex(line, "  ")
		if idx < 0 {
			return "", "", "", fmt.Errorf("malformed changelog trailer (no date separator): %q", line)
		}
		date = strings.TrimSpace(line[idx+2:])
		if date == "" {
			return "", "", "", fmt.Errorf("empty date in changelog trailer: %q", line)
		}
		return name, baseVersion, date, nil
	}
	return "", "", "", fmt.Errorf("no trailer found in changelog top entry")
}

// formatLocalChangelogEntry builds a synthetic changelog entry stamping the
// content-derived local version onto the package, authored by nobody, with the
// date copied from the existing top entry so dpkg-buildpackage reads the same
// SOURCE_DATE_EPOCH it would have otherwise.
func formatLocalChangelogEntry(srcName, version, date string) string {
	return srcName + " (" + version + ") UNRELEASED; urgency=medium\n" +
		"\n" +
		"  * Local build.\n" +
		"\n" +
		" -- nobody <nobody@localhost>  " + date + "\n" +
		"\n"
}

// stampLocalChangelog prepends the synthetic local-version entry to the
// source's debian/changelog inside the build's mount namespace, leaving the
// existing history untouched. srcDst is the source directory's rootfs path.
func (s *DebianPkgBuild) stampLocalChangelog(mountNS *container.MountNS, srcDst string) error {
	srcName, _, date, err := s.parseChangelogTopEntry()
	if err != nil {
		return fmt.Errorf("parse changelog top entry: %w", err)
	}
	version, err := s.computeVersion()
	if err != nil {
		return fmt.Errorf("compute local version: %w", err)
	}
	hostChangelog, err := os.ReadFile(filepath.Join(s.PkgDir, "src", "debian", "changelog"))
	if err != nil {
		return fmt.Errorf("read changelog: %w", err)
	}

	newContent := append([]byte(formatLocalChangelogEntry(srcName, version, date)), hostChangelog...)
	f, err := mountNS.Open(srcDst+"/debian/changelog", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("open changelog for write: %w", err)
	}
	_, err = f.Write(newContent)
	f.Close()
	return err
}

// PackAsRootfsTar tars dir deterministically, gzips it, and stores it as a blob.
func PackAsRootfsTar(dir string, store *objstore.Store) (objstore.Hash, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)

	if err := stream.TarCreate(dir, gw); err != nil {
		gw.Close()
		return objstore.Hash{}, fmt.Errorf("create tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return objstore.Hash{}, fmt.Errorf("close gzip: %w", err)
	}
	hash, err := store.Blobs.Store(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("store tar: %w", err)
	}
	return hash, nil
}
