package build

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/debian/deb822"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// SBOM writes the SPDX document for a shipped Debian source build.
func (s *DebianPkgBuild) SBOM(sctx artifact.SBOMContext) ([]byte, error) {
	return writeSourceBuildSBOM(sourceBuildSBOM{
		kind:        "debian-pkg-build",
		src:         s,
		depBinaries: depBinaryArtifacts(s.DepArtifacts),
	}, sctx)
}

// SBOM writes the SPDX document for an image-assembly tooling build. It reuses the
// shared source-build writer on the inner build; the only differences are the
// SPDX build type and that its chroot deps are other tooling builds, whose whole
// .deb output set populates the chroot.
func (t *ToolingPkgBuild) SBOM(sctx artifact.SBOMContext) ([]byte, error) {
	var deps []chrootDep
	for _, d := range t.buildDeps {
		for _, bin := range ParseBinaryPackageNames(d.PkgDir) {
			deps = append(deps, chrootDep{producer: d, binName: bin, debName: bin + ".deb"})
		}
	}
	return writeSourceBuildSBOM(sourceBuildSBOM{
		kind:        "tooling-pkg-build",
		src:         t.inner,
		depBinaries: deps,
	}, sctx)
}

// sourceBuildSBOM carries the inputs to the shared source-build writer, used by
// both the shipped DebianPkgBuild and (through its inner build) ToolingPkgBuild.
type sourceBuildSBOM struct {
	kind string          // SPDX build type: debian-pkg-build or tooling-pkg-build
	src  *DebianPkgBuild // the (inner) source build carrying PkgDir/Name/Arch/store
	// depBinaries are the locally-built package artifacts whose .debs populate the
	// build chroot (shipped: binary-package deps; tooling: tooling-build deps,
	// mapped to a uniform producer accessor). A chroot package whose name matches
	// one of these is imported from its producer rather than minted locally.
	depBinaries []chrootDep
}

// chrootDep is a locally-built chroot package: the producing artifact (for the
// cross-document import) and the name of the .deb output it contributes.
type chrootDep struct {
	producer artifact.Artifact
	binName  string
	debName  string // output name in the producer's manifest, e.g. "<bin>.deb"
}

// depBinaryArtifacts turns a shipped source build's DepArtifacts (which are
// *debianBinaryPkg) into chrootDep records.
func depBinaryArtifacts(deps []artifact.Artifact) []chrootDep {
	var out []chrootDep
	for _, d := range deps {
		if bp, ok := d.(*debianBinaryPkg); ok {
			out = append(out, chrootDep{producer: bp.sourceBuild, binName: bp.name, debName: bp.name + ".deb"})
		}
	}
	return out
}

// writeSourceBuildSBOM is the shared source-build / tooling-build SPDX writer.
func writeSourceBuildSBOM(sb sourceBuildSBOM, sctx artifact.SBOMContext) ([]byte, error) {
	s := sb.src
	store := sctx.Store
	d := newSPDXDoc(sctx.Namespace, sctx.Manifest)

	agent := d.addAgent()
	build := d.addArtifactBuild(sb.kind, s.Arch, buildTargetKey(sb.kind, s.Name, s.Arch), agent)

	// --- source materials -------------------------------------------------
	srcVersion := sourceVersion(s)
	source := d.sourcePackage("source", s.Name, srcVersion, glbxVendor)
	d.relate(build, "hasInput", source, "build")

	// Orig tarball(s): each a checksummed software_File the logical source
	// contains. The build reaches them through the source (hasInput source →
	// source contains orig), so no direct build→orig edge.
	if refs, err := buildcfg.LoadSourcesYML(s.PkgDir); err == nil {
		for _, ref := range refs {
			if strings.HasSuffix(ref.File, ".asc") || strings.HasSuffix(ref.File, ".sig") {
				continue
			}
			fid := d.fileElement("inputs/file/"+segEscape(ref.File)+"/"+ref.Hash.String(), ref.File, ref.Hash)
			d.relate(source, "contains", fid, "")
		}
	}

	// Debian packaging directory: a source-purpose package by VCS externalRef,
	// reached through the source (no direct build→packaging edge).
	gitRef := resolveGitSourceRef(s.PkgDir)
	pkgPath := gitRef.subdir
	if pkgPath == "" {
		pkgPath = "pkgs/" + filepath.Base(s.PkgDir) + "/src/debian"
	}
	packaging := d.gitPackagingElement("inputs/git/packaging", pkgPath, gitRef.repo, gitRef.commit, gitRef.subdir)
	d.relate(source, "contains", packaging, "")

	// build.yml recipe: a software_File, hashed by its own bytes, that only
	// configures the build (no separate hasInput edge).
	buildYMLPath := filepath.Join(s.PkgDir, "build.yml")
	if bh, err := hashFileBytes(store, buildYMLPath); err == nil {
		rel := buildYMLDocName(s.PkgDir)
		fid := d.fileElement("inputs/file/"+segEscape(rel)+"/"+bh.String(), rel, bh)
		d.relate(fid, "configures", build, "build")
	}

	// --- build container rootfs -------------------------------------------
	chrootEnv := d.environment("build-container-rootfs", "build container rootfs", "container")
	d.relate(build, "usesTool", chrootEnv, "build")

	chrootPkgs, err := recomputeChrootPkgs(s, store)
	if err != nil {
		return nil, fmt.Errorf("recompute chroot: %w", err)
	}
	// Map locally-built dep binary names to their producer import, so a chroot
	// package that is one of our deps references the producer's .deb element.
	localByName := map[string]chrootDep{}
	for _, dep := range sb.depBinaries {
		localByName[dep.binName] = dep
	}
	for _, pkg := range chrootPkgs {
		var toolID string
		if dep, ok := localByName[pkg.Name]; ok {
			pm, dc, found, err := producerManifest(dep.producer, store, sctx.Namespace)
			if err != nil {
				return nil, err
			}
			toolID = d.importElement(pm, "outputs/"+segEscape(dep.debName), dc, found)
		} else {
			sha, _ := objstore.NewHash(pkg.SHA256)
			path := fmt.Sprintf("inputs/deb/%s/%s/%s/%s", segEscape(pkg.Name), segEscape(pkg.Version), segEscape(pkg.Architecture), pkg.SHA256)
			toolID = d.debPackage(path, pkg.Name, pkg.Version, pkg.Architecture, debVendor, sha)
		}
		// The build reaches each package only through the container (usesTool →
		// container → contains → package), no direct build→package edge.
		d.relate(chrootEnv, "contains", toolID, "")
	}

	// --- outputs ----------------------------------------------------------
	if err := emitPackageOutputs(d, build, sctx.Outputs, store, glbxVendor); err != nil {
		return nil, err
	}

	return d.marshal()
}

// emitPackageOutputs emits the <bin>.deb software_Package and control:<bin>
// software_File for each produced binary, with hasOutput/hasMetadata and the
// static-link edges read from each binary's own control stanza. The build
// element is the output anchor (no separate result→contains edge). It is shared
// by the source build and (via re-emission) referenced by binary validation.
func emitPackageOutputs(d *spdxDoc, build string, outputs []artifact.Output, store *objstore.Store, vendor string) error {
	// Index outputs by name so a .deb can find its control:<name> sibling.
	byName := map[string]objstore.Hash{}
	for _, o := range outputs {
		byName[o.Name] = o.Hash
	}
	// Deterministic order.
	names := make([]string, 0, len(outputs))
	for _, o := range outputs {
		names = append(names, o.Name)
	}
	sort.Strings(names)

	for _, name := range names {
		if !strings.HasSuffix(name, ".deb") {
			continue
		}
		bin := strings.TrimSuffix(name, ".deb")
		debHash := byName[name]
		controlHash, hasControl := byName["control:"+bin]

		var version, arch string
		var stanza deb822.Stanza
		if hasControl {
			st, err := readControlStanza(store, controlHash)
			if err == nil {
				stanza = st
				version = st["version"]
				arch = st["architecture"]
			}
		}
		if arch == "" {
			arch = "all"
		}
		if version == "" {
			version = "0"
		}

		debID := d.debPackage("outputs/"+segEscape(name), bin, version, arch, vendor, debHash)
		d.relate(build, "hasOutput", debID, "build")

		if hasControl {
			ctlID := d.fileElement("outputs/"+segEscape("control:"+bin), "control:"+bin, controlHash)
			d.relate(build, "hasOutput", ctlID, "build")
			d.relate(debID, "hasMetadata", ctlID, "")
		}

		// Static-link edges from Built-Using / Static-Built-Using.
		for _, decl := range staticBuiltUsing(stanza) {
			linkPath := fmt.Sprintf("static-link/%s/%s", segEscape(decl.name), segEscape(decl.version))
			linkID := d.sourcePackage(linkPath, decl.name, decl.version, debVendor)
			d.relate(debID, "hasStaticLink", linkID, "build")
		}
	}
	return nil
}

// --- helpers --------------------------------------------------------------

// buildTargetKey is the display name of the result package: the artifact's build
// target key (matching Key() on each type).
func buildTargetKey(kind, name, arch string) string {
	return fmt.Sprintf("%s:%s:%s", kind, name, arch)
}

// sourceVersion returns the effective (local-stamped) version of the source
// package, matching the version dpkg-buildpackage stamped onto the outputs.
func sourceVersion(s *DebianPkgBuild) string {
	if v, err := s.computeVersion(); err == nil {
		return v
	}
	return "0"
}

// recomputeChrootPkgs reproduces the build chroot package set exactly as Build
// assembles it: pinned lockfile tooling minus locally-overridden names, plus the
// locally-built dependency binaries. Deterministic given the store.
func recomputeChrootPkgs(s *DebianPkgBuild, store *objstore.Store) ([]*index.Package, error) {
	s.resolveDeps()
	localPkgs := s.buildLocalIndexForBuild(store).All()
	localNames := make(map[string]bool, len(localPkgs))
	for _, pkg := range localPkgs {
		localNames[pkg.Name] = true
	}
	var chroot []*index.Package
	// The pinned lockfile may be absent for a degenerate/test package; its absence
	// just means no external tooling to enumerate, not a failure.
	if lockIdx, err := s.loadLockfileIndex(); err == nil {
		for _, pkg := range lockIdx.All() {
			if !localNames[pkg.Name] {
				chroot = append(chroot, pkg)
			}
		}
	}
	chroot = append(chroot, localPkgs...)
	sort.Slice(chroot, func(i, j int) bool { return chroot[i].Name < chroot[j].Name })
	return chroot, nil
}

// buildYMLDocName is the repo-relative display name of a package's build.yml,
// "pkgs/<name>/build.yml", derived from the git toplevel when available.
func buildYMLDocName(pkgDir string) string {
	full := filepath.Join(pkgDir, "build.yml")
	if top := gitOutput(pkgDir, "rev-parse", "--show-toplevel"); top != "" {
		if rel, err := filepath.Rel(top, full); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return "pkgs/" + filepath.Base(pkgDir) + "/build.yml"
}

// hashFileBytes reads a file and stores its bytes in the object store, returning
// the content hash. Used to pin build.yml by its own bytes. The blob is a
// harmless side effect (content-addressed; identical bytes are a no-op).
func hashFileBytes(store *objstore.Store, path string) (objstore.Hash, error) {
	f, err := os.Open(path)
	if err != nil {
		return objstore.Hash{}, err
	}
	defer f.Close()
	return store.Blobs.Store(f)
}

// readControlStanza opens a stored control blob and parses its first stanza.
func readControlStanza(store *objstore.Store, h objstore.Hash) (deb822.Stanza, error) {
	r, err := store.Blobs.Open(h)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return deb822.NewReader(r).Next()
}

// staticDecl is one declared static-link source package: name and version.
type staticDecl struct{ name, version string }

// staticBuiltUsing parses the union of a control stanza's Built-Using and
// Static-Built-Using fields into distinct (name, version) declarations. deb822
// lowercases keys. Each field is a comma list of "name (= version)" clauses.
func staticBuiltUsing(stanza deb822.Stanza) []staticDecl {
	if stanza == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []staticDecl
	for _, field := range []string{"built-using", "static-built-using"} {
		v := strings.TrimSpace(stanza[field])
		if v == "" {
			continue
		}
		for _, clause := range strings.Split(v, ",") {
			name, version := parseBuiltUsingClause(clause)
			if name == "" {
				continue
			}
			key := name + "\x00" + version
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, staticDecl{name: name, version: version})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].name != out[j].name {
			return out[i].name < out[j].name
		}
		return out[i].version < out[j].version
	})
	return out
}

// parseBuiltUsingClause parses "name (= version)" (or bare "name") into its parts.
func parseBuiltUsingClause(clause string) (name, version string) {
	clause = strings.TrimSpace(clause)
	if clause == "" {
		return "", ""
	}
	open := strings.Index(clause, "(")
	if open < 0 {
		return clause, ""
	}
	name = strings.TrimSpace(clause[:open])
	rest := clause[open+1:]
	if close := strings.Index(rest, ")"); close >= 0 {
		rest = rest[:close]
	}
	// rest is like "= 1.2"; strip a leading relational operator.
	rest = strings.TrimSpace(rest)
	rest = strings.TrimLeft(rest, "=<>")
	version = strings.TrimSpace(rest)
	return name, version
}
