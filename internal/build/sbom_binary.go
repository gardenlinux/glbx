package build

import (
	"fmt"
	"sort"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// SBOM writes the SPDX document for a binary-package validation. The validation
// re-emits the source build's selected .deb and control byte-for-byte, so this
// document imports the source build's output elements and records copiedTo edges
// to its own re-emitted outputs, rather than describing the payload from scratch.
func (b *debianBinaryPkg) SBOM(sctx artifact.SBOMContext) ([]byte, error) {
	store := sctx.Store
	b.resolveExtraDeps()
	d := newSPDXDoc(sctx.Namespace, sctx.Manifest)

	agent := d.addAgent()
	build := d.addArtifactBuild("debian-binary-pkg", b.sourceBuild.Arch, b.Key(), agent)

	// Import the source build's selected .deb and control, and record them as build
	// inputs. The producer element URIs are minted identically on both sides.
	srcManifest, srcDoc, srcFound, err := producerManifest(b.sourceBuild, store, sctx.Namespace)
	if err != nil {
		return nil, err
	}
	srcDebElem := d.importElement(srcManifest, "outputs/"+segEscape(b.name+".deb"), srcDoc, srcFound)
	srcCtlElem := d.importElement(srcManifest, "outputs/"+segEscape("control:"+b.name), srcDoc, srcFound)
	d.relate(build, "hasInput", srcDebElem, "build")
	d.relate(build, "hasInput", srcCtlElem, "build")

	// Re-emitted outputs: byte-identical to the source build's, minted under this
	// validation's manifest hash. emitPackageOutputs also reads the control stanza
	// to attach this binary's own static-link edges.
	if err := emitPackageOutputs(d, build, sctx.Outputs, store, glbxVendor); err != nil {
		return nil, err
	}
	// copiedTo from each source-build output to the re-emitted output of the same
	// name. The re-emitted element IDs are what emitPackageOutputs minted locally.
	d.relate(srcDebElem, "copiedTo", d.spdxID("outputs/"+segEscape(b.name+".deb")), "")
	if _, ok := outputHashByName(sctx.Outputs, "control:"+b.name); ok {
		d.relate(srcCtlElem, "copiedTo", d.spdxID("outputs/"+segEscape("control:"+b.name)), "")
	}

	// Validation bootstrap container: every package dpkg reasons over during the
	// install check is reached through the bootstrap environment (usesTool →
	// environment → contains → package), no direct build→package edge.
	bootstrapEnv := d.environment("bootstrap", "bootstrap environment", "container")
	d.relate(build, "usesTool", bootstrapEnv, "build")

	chroot, err := recomputeInstallCheckChroot(b, store)
	if err != nil {
		return nil, fmt.Errorf("recompute install-check chroot: %w", err)
	}
	for _, pkg := range chroot {
		sha, _ := objstore.NewHash(pkg.SHA256)
		path := fmt.Sprintf("inputs/deb/%s/%s/%s/%s", segEscape(pkg.Name), segEscape(pkg.Version), segEscape(pkg.Architecture), pkg.SHA256)
		toolID := d.debPackage(path, pkg.Name, pkg.Version, pkg.Architecture, debVendor, sha)
		d.relate(bootstrapEnv, "contains", toolID, "")
	}

	return d.marshal()
}

// recomputeInstallCheckChroot reproduces the package set present in the binary's
// install-check sandbox: the pinned bootstrap tooling (the source build's
// build-deps essential set) plus this binary's locally-built runtime closure. It
// mirrors installCheck's index assembly so the usesTool set matches what dpkg
// actually reasoned over.
func recomputeInstallCheckChroot(b *debianBinaryPkg, store *objstore.Store) ([]*index.Package, error) {
	byName := map[string]*index.Package{}
	// The bootstrap tooling lockfile may be absent for a degenerate/test package;
	// treat that as "no bootstrap base to enumerate" rather than a failure.
	if bootstrap, err := b.sourceBuild.loadLockfileIndex(); err == nil {
		for _, pkg := range bootstrap.All() {
			byName[pkg.Name] = pkg
		}
	}
	// The binary's local runtime closure (what gets installed on top of the
	// bootstrap base), from the source-build manifests of its closure binaries.
	local := b.buildLocalIndex(store)
	for _, pkg := range local.All() {
		byName[pkg.Name] = pkg
	}
	out := make([]*index.Package, 0, len(byName))
	for _, pkg := range byName {
		if pkg.SHA256 == "" {
			continue
		}
		out = append(out, pkg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// outputHashByName returns the hash of the named output, if present.
func outputHashByName(outputs []artifact.Output, name string) (objstore.Hash, bool) {
	for _, o := range outputs {
		if o.Name == name {
			return o.Hash, true
		}
	}
	return objstore.Hash{}, false
}
