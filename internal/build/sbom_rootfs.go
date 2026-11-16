package build

import (
	"fmt"
	"sort"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// SBOM writes the SPDX document for a root filesystem. The shipped packages
// (Layer 0 + Layer 2) are reached through the payload the rootfs archive expands
// to, each imported from its producing binary-package SBOM; the discarded Layer 1
// configuration tooling is described locally under the layer1 environment.
func (r *Rootfs) SBOM(sctx artifact.SBOMContext) ([]byte, error) {
	store := sctx.Store
	d := newSPDXDoc(sctx.Namespace, sctx.Manifest)

	agent := d.addAgent()
	build := d.addArtifactBuild("rootfs", r.Arch, r.Key(), agent)

	// The sole output: rootfs.tar.gz. Record it, and the payload it expands to.
	tarHash, ok := outputHashByName(sctx.Outputs, "rootfs.tar.gz")
	if !ok {
		return nil, fmt.Errorf("rootfs SBOM: no rootfs.tar.gz output")
	}
	tarID := d.outputFile("outputs/"+segEscape("rootfs.tar.gz"), "rootfs.tar.gz", tarHash)
	d.relate(build, "hasOutput", tarID, "build")
	payload := d.payload("root filesystem")
	d.relate(tarID, "expandsTo", payload, "")

	// Recompute the shipped install set (Layer 0 + Layer 2) and the discarded
	// Layer 1 tooling, exactly as Build does.
	localPkgs, err := r.collectLocalPkgs(sctx.Inputs)
	if err != nil {
		return nil, err
	}
	installPkgs, err := r.narrowInstallSet(localPkgs, store)
	if err != nil {
		return nil, err
	}
	installNames := make(map[string]bool, len(installPkgs))
	for _, lp := range installPkgs {
		installNames[lp.name] = true
	}
	layer1Pkgs, err := r.loadLayer1Pkgs(installNames)
	if err != nil {
		// A missing rootfs-deps.yml (degenerate/test config) means no Layer-1
		// configuration tooling to enumerate, not a failure.
		layer1Pkgs = nil
	}

	// Bridge each shipped package name to its producing binary-package artifact so
	// the payload contains the producer's own .deb element (cross-document import),
	// matching the URIs that binary package's SBOM minted for its output.
	binByName := map[string]*debianBinaryPkg{}
	for _, bp := range r.allBinaryDeps() {
		binByName[bp.name] = bp
	}
	shipped := make([]string, 0, len(installPkgs))
	for _, lp := range installPkgs {
		shipped = append(shipped, lp.name)
	}
	sort.Strings(shipped)
	for _, name := range shipped {
		bp := binByName[name]
		if bp == nil {
			// No producing artifact (should not happen for a shipped local pkg);
			// skip rather than mint a bogus reference.
			continue
		}
		pm, dc, found, err := producerManifest(bp, store, sctx.Namespace)
		if err != nil {
			return nil, err
		}
		elem := d.importElement(pm, "outputs/"+segEscape(bp.name+".deb"), dc, found)
		d.relate(payload, "contains", elem, "")
		d.relate(build, "hasInput", elem, "build")
	}

	// Layer 1: the discarded configuration tooling, described locally. Each tool
	// is reached through the layer1 environment (usesTool → environment →
	// contains → package), no direct build→package edge.
	layer1Env := d.environment("layer1", "Layer 1 configuration tooling (discarded)", "container")
	d.relate(build, "usesTool", layer1Env, "build")
	sort.Slice(layer1Pkgs, func(i, j int) bool { return layer1Pkgs[i].Name < layer1Pkgs[j].Name })
	for _, pkg := range layer1Pkgs {
		if pkg.SHA256 == "" {
			continue
		}
		sha, _ := objstore.NewHash(pkg.SHA256)
		path := fmt.Sprintf("inputs/deb/%s/%s/%s/%s", segEscape(pkg.Name), segEscape(pkg.Version), segEscape(pkg.Architecture), pkg.SHA256)
		toolID := d.debPackage(path, pkg.Name, pkg.Version, pkg.Architecture, debVendor, sha)
		d.relate(layer1Env, "contains", toolID, "")
	}

	return d.marshal()
}
