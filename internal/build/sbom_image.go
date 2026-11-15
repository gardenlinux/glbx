package build

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// SBOM writes the SPDX document for an assembled image. It imports the rootfs
// payload and any included images (disk includes initrd → a contains edge to the
// initrd's image output), describes the assembly tooling, and records the recipe
// and literal inputs the assembly script consumed.
func (im *Image) SBOM(sctx artifact.SBOMContext) ([]byte, error) {
	store := sctx.Store
	d := newSPDXDoc(sctx.Namespace, sctx.Manifest)

	agent := d.addAgent()
	build := d.addArtifactBuild("image", im.Arch, im.Key(), agent)

	// The sole output: the assembled image blob.
	imgHash, ok := outputHashByName(sctx.Outputs, imageOutputName)
	if !ok {
		return nil, fmt.Errorf("image SBOM: no %q output", imageOutputName)
	}
	imgID := d.outputFile("outputs/"+segEscape(imageOutputName), imageOutputName, imgHash)
	d.relate(build, "hasOutput", imgID, "build")

	// Rootfs input: import the rootfs producer's payload and record it as an input.
	// The delivered image carries that filesystem, so the image output contains it.
	rootfsManifest, rootfsDoc, err := producerManifest(im.rootfs, store, sctx.Namespace)
	if err != nil {
		return nil, err
	}
	rootfsPayload := d.importElement(rootfsManifest, "payload", rootfsDoc)
	d.relate(build, "hasInput", rootfsPayload, "build")
	d.relate(imgID, "contains", rootfsPayload, "")

	// Included images (e.g. disk includes initrd): each becomes a contains edge to
	// the included image's own image output, imported under that image's manifest.
	seenImg := map[string]bool{}
	var imgDeps []*Image
	for _, in := range im.inputs {
		if in.imageDep == nil || seenImg[in.imageDep.Key()] {
			continue
		}
		seenImg[in.imageDep.Key()] = true
		imgDeps = append(imgDeps, in.imageDep)
	}
	sort.Slice(imgDeps, func(i, j int) bool { return imgDeps[i].Key() < imgDeps[j].Key() })
	for _, dep := range imgDeps {
		pm, dc, err := producerManifest(dep, store, sctx.Namespace)
		if err != nil {
			return nil, err
		}
		elem := d.importElement(pm, "outputs/"+segEscape(imageOutputName), dc)
		d.relate(build, "hasInput", elem, "build")
		d.relate(imgID, "contains", elem, "")
	}

	// Literal image inputs (value, not an image): a software_File the recipe read.
	for _, in := range im.inputs {
		if in.value == nil {
			continue
		}
		vh, err := store.Blobs.Store(strings.NewReader(*in.value))
		if err != nil {
			return nil, fmt.Errorf("store image input %q: %w", in.label, err)
		}
		fid := d.fileElement("inputs/value/"+segEscape(in.label)+"/"+vh.String(), in.label, vh)
		d.relate(build, "hasInput", fid, "build")
	}

	// Assembly recipe: the type's build script and conf.yml, each a software_File
	// that only configures the build (no separate hasInput edge).
	for _, rp := range []struct{ path, role string }{
		{im.scriptPath(), "build"},
		{im.confPath(), "conf.yml"},
	} {
		if h, err := hashFileBytes(store, rp.path); err == nil {
			name := recipeDocName(im, rp.path)
			fid := d.fileElement("inputs/file/"+segEscape(name)+"/"+h.String(), name, h)
			d.relate(fid, "configures", build, "build")
		}
	}

	// Assembly container: pinned assembly tools + local-tooling builds, each
	// reached through the assembly environment (usesTool → environment →
	// contains → package), no direct build→package edge.
	assemblyEnv := d.environment("assembly", "assembly container", "container")
	d.relate(build, "usesTool", assemblyEnv, "build")

	if pinned, err := im.loadAssemblyTooling(); err == nil {
		sort.Slice(pinned, func(i, j int) bool { return pinned[i].Name < pinned[j].Name })
		for _, pkg := range pinned {
			if pkg.SHA256 == "" {
				continue
			}
			sha, _ := objstore.NewHash(pkg.SHA256)
			path := fmt.Sprintf("inputs/deb/%s/%s/%s/%s", segEscape(pkg.Name), segEscape(pkg.Version), segEscape(pkg.Architecture), pkg.SHA256)
			toolID := d.debPackage(path, pkg.Name, pkg.Version, pkg.Architecture, debVendor, sha)
			d.relate(assemblyEnv, "contains", toolID, "")
		}
	}

	// Local-tooling .debs: import each from its producing ToolingPkgBuild.
	local := append([]localToolingRef(nil), im.local...)
	sort.Slice(local, func(i, j int) bool { return local[i].output < local[j].output })
	for _, l := range local {
		pm, dc, err := producerManifest(l.build, store, sctx.Namespace)
		if err != nil {
			return nil, err
		}
		elem := d.importElement(pm, "outputs/"+segEscape(l.output), dc)
		d.relate(assemblyEnv, "contains", elem, "")
	}

	return d.marshal()
}

// recipeDocName is the repo-relative display name of an image recipe file, e.g.
// images.d/disk/build, derived from the config-repo root (im.baseDir) when the
// path is under it.
func recipeDocName(im *Image, path string) string {
	if im.baseDir != "" {
		if rel, err := filepath.Rel(im.baseDir, path); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}
