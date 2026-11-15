package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// mustWrite writes content to path, creating parent dirs.
func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// storeManifestAndSBOM mimics the engine's post-build path for one artifact: it
// folds provenance into the outputs, writes the manifest blob + identity→manifest
// map entry, invokes the artifact's SBOM writer with a fully populated
// SBOMContext, and stores the SBOM blob under its map key. It returns the
// output-manifest hash and the serialized SBOM document. Inputs are the resolved
// build inputs the writer may consult.
func storeManifestAndSBOM(t *testing.T, store *objstore.Store, a artifact.Artifact, outputs []artifact.Output, inputs map[string]objstore.Hash) (objstore.Hash, []byte) {
	t.Helper()
	identity, err := a.Identity()
	if err != nil {
		t.Fatalf("%s identity: %v", a.Key(), err)
	}
	manifestHash := storeSerializedManifest(t, store, outputs)
	if err := store.Map.Set(identity, manifestHash, false); err != nil {
		t.Fatalf("set manifest map: %v", err)
	}
	doc, err := a.SBOM(artifact.SBOMContext{
		Ctx:       context.Background(),
		Store:     store,
		Inputs:    inputs,
		Outputs:   outputs,
		Identity:  identity,
		Manifest:  manifestHash,
		Namespace: "localhost",
	})
	if err != nil {
		t.Fatalf("%s SBOM: %v", a.Key(), err)
	}
	blobHash, err := store.Blobs.Store(strings.NewReader(string(doc)))
	if err != nil {
		t.Fatalf("store sbom blob: %v", err)
	}
	if err := store.Map.Set(artifact.SBOMMapKey("localhost", manifestHash), blobHash, false); err != nil {
		t.Fatalf("set sbom map: %v", err)
	}
	return manifestHash, doc
}

func storeSerializedManifest(t *testing.T, store *objstore.Store, outputs []artifact.Output) objstore.Hash {
	t.Helper()
	h, err := store.Blobs.Store(strings.NewReader(artifact.SerializeManifest(outputs)))
	if err != nil {
		t.Fatalf("store manifest: %v", err)
	}
	return h
}

// spdxGraph is a minimal parse of a serialized SPDX document for assertions.
type spdxGraph struct {
	Context string           `json:"@context"`
	Graph   []map[string]any `json:"@graph"`
}

func parseSPDX(t *testing.T, doc []byte) spdxGraph {
	t.Helper()
	var g spdxGraph
	if err := json.Unmarshal(doc, &g); err != nil {
		t.Fatalf("parse spdx: %v\n%s", err, doc)
	}
	if g.Context == "" || len(g.Graph) == 0 {
		t.Fatalf("spdx missing @context/@graph: %s", doc)
	}
	return g
}

// ids returns the set of spdxId values present as elements in the graph.
func (g spdxGraph) ids() map[string]bool {
	out := map[string]bool{}
	for _, el := range g.Graph {
		if id, ok := el["spdxId"].(string); ok {
			out[id] = true
		}
	}
	return out
}

// relationships returns every relationship element as (from, type, to) triples.
func (g spdxGraph) relationships() [][3]string {
	var out [][3]string
	for _, el := range g.Graph {
		rt, ok := el["relationshipType"].(string)
		if !ok {
			continue
		}
		from, _ := el["from"].(string)
		tos, _ := el["to"].([]any)
		for _, to := range tos {
			if s, ok := to.(string); ok {
				out = append(out, [3]string{from, rt, s})
			}
		}
	}
	return out
}

func (g spdxGraph) hasRel(from, rt, to string) bool {
	for _, r := range g.relationships() {
		if r[0] == from && r[1] == rt && r[2] == to {
			return true
		}
	}
	return false
}

// importMaps returns the SpdxDocument.import ExternalMap records.
func (g spdxGraph) importMaps() []map[string]any {
	for _, el := range g.Graph {
		if el["type"] == "SpdxDocument" {
			if imp, ok := el["import"].([]any); ok {
				out := make([]map[string]any, 0, len(imp))
				for _, m := range imp {
					if mm, ok := m.(map[string]any); ok {
						out = append(out, mm)
					}
				}
				return out
			}
		}
	}
	return nil
}

// TestSBOMCrossDocumentURIConsistency builds a source package and its binary
// validation through the real writers and asserts the binary document references
// the source document's output element by a byte-identical URI, pins the source
// document by checksum, and records the copiedTo/hasInput edges against it.
func TestSBOMCrossDocumentURIConsistency(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	srcDir := filepath.Join(root, "pkgs", "fixture")
	writeSourcePkg(t, srcDir, "Source: fixture\nPackage: fixture\n", "")

	src := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "fixture", PkgDir: srcDir, Arch: "amd64", Store: store})
	bin := src.Binary("fixture")
	bin.depsResolved = true // no deps; skip build.yml resolution

	// Plant the source build's outputs: fixture.deb + control:fixture.
	debBlob, _ := store.Blobs.Store(strings.NewReader("fake deb bytes"))
	ctlBlob := storeControl(t, store, "Package: fixture\nVersion: 1\nArchitecture: all\nStatic-Built-Using: go-helper (= 1.2), bundled-lib (= 3.0)\n")
	srcOutputs := []artifact.Output{
		{Name: "fixture.deb", Hash: debBlob},
		{Name: "control:fixture", Hash: ctlBlob},
	}
	srcManifest, srcDoc := storeManifestAndSBOM(t, store, src, srcOutputs, nil)

	// The binary re-emits the same blobs; its inputs come from the source build.
	binInputs := map[string]objstore.Hash{"fixture.deb": debBlob, "control:fixture": ctlBlob}
	binOutputs := []artifact.Output{
		{Name: "fixture.deb", Hash: debBlob},
		{Name: "control:fixture", Hash: ctlBlob},
	}
	_, binDoc := storeManifestAndSBOM(t, store, bin, binOutputs, binInputs)

	sg := parseSPDX(t, srcDoc)
	bg := parseSPDX(t, binDoc)

	// The exact URI the source document minted for its .deb output.
	srcPrefix := "urn:spdx:localhost/glbx/sboms/" + srcManifest.String()
	srcDebURI := srcPrefix + "/outputs/fixture.deb"
	if !sg.ids()[srcDebURI] {
		t.Fatalf("source doc missing its own output element %s", srcDebURI)
	}

	// The binary document must reference that SAME URI via an ExternalMap, and pin
	// the source document's bytes by checksum.
	imports := bg.importMaps()
	var found bool
	for _, im := range imports {
		if im["externalSpdxId"] == srcDebURI {
			found = true
			// definingArtifact points at a documents/<srcManifest> file whose
			// checksum is the source document's bytes.
			def, _ := im["definingArtifact"].(string)
			wantDef := "urn:spdx:localhost/glbx/sboms/" + bg.resultManifest(t) + "/documents/" + srcManifest.String()
			if def != wantDef {
				t.Errorf("definingArtifact = %s, want %s", def, wantDef)
			}
			if lh, _ := im["locationHint"].(string); lh != srcManifest.String()+".spdx.json" {
				t.Errorf("locationHint = %s, want %s.spdx.json", lh, srcManifest.String())
			}
		}
	}
	if !found {
		t.Fatalf("binary doc does not import source output URI %s; imports=%v", srcDebURI, imports)
	}

	// The binary build must have a hasInput edge to that foreign URI, and a
	// copiedTo from it to the binary's own re-emitted output.
	binResultPrefix := "urn:spdx:localhost/glbx/sboms/" + bg.resultManifest(t)
	if !bg.hasRel(binResultPrefix+"/artifact-build", "hasInput", srcDebURI) {
		t.Errorf("binary build missing hasInput -> %s", srcDebURI)
	}
	if !bg.hasRel(srcDebURI, "copiedTo", binResultPrefix+"/outputs/fixture.deb") {
		t.Errorf("binary doc missing copiedTo %s -> own output", srcDebURI)
	}

	// The defining-file checksum must equal the stored source document bytes.
	defFileChecksum := bg.definingFileChecksum(t, srcManifest.String())
	wantChecksum := sha256HexBytes(srcDoc)
	if defFileChecksum != wantChecksum {
		t.Errorf("pinned source-doc checksum = %s, want %s (sha256 of source doc bytes)", defFileChecksum, wantChecksum)
	}

	// Static-link edges from the control stanza must appear on the source build's
	// .deb (both Static-Built-Using entries).
	for _, decl := range []struct{ name, ver string }{{"bundled-lib", "3.0"}, {"go-helper", "1.2"}} {
		link := srcPrefix + "/static-link/" + decl.name + "/" + decl.ver
		if !sg.hasRel(srcDebURI, "hasStaticLink", link) {
			t.Errorf("source doc missing hasStaticLink %s -> %s", srcDebURI, link)
		}
	}
}

// resultManifest extracts the output-manifest hash from a document's own result
// element prefix (urn:spdx:localhost/glbx/sboms/<M>/result).
func (g spdxGraph) resultManifest(t *testing.T) string {
	t.Helper()
	for _, el := range g.Graph {
		if el["type"] == "software_Sbom" {
			re, _ := el["rootElement"].([]any)
			if len(re) == 1 {
				s, _ := re[0].(string)
				// s == urn:spdx:localhost/glbx/sboms/<M>/result
				const marker = "/glbx/sboms/"
				i := strings.Index(s, marker)
				rest := s[i+len(marker):]
				return rest[:strings.Index(rest, "/")]
			}
		}
	}
	t.Fatalf("no software_Sbom root in graph")
	return ""
}

// definingFileChecksum returns the sha256 hashValue of the documents/<N> file.
func (g spdxGraph) definingFileChecksum(t *testing.T, producerManifest string) string {
	t.Helper()
	for _, el := range g.Graph {
		id, _ := el["spdxId"].(string)
		if strings.HasSuffix(id, "/documents/"+producerManifest) {
			vs, _ := el["verifiedUsing"].([]any)
			for _, v := range vs {
				vm, _ := v.(map[string]any)
				if hv, ok := vm["hashValue"].(string); ok {
					return hv
				}
			}
		}
	}
	t.Fatalf("no documents/%s element", producerManifest)
	return ""
}

// TestSBOMRootfsImportsBinaryOutputs builds a binary package then a rootfs over
// it, and asserts the rootfs document's payload contains the binary's own
// .deb output element by a byte-identical cross-document URI.
func TestSBOMRootfsImportsBinaryOutputs(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	srcDir := filepath.Join(root, "pkgs", "glibc")
	writeSourcePkg(t, srcDir, "Source: glibc\nPackage: libc6\n", "")

	src := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "glibc", PkgDir: srcDir, Arch: "amd64", Store: store})
	bin := src.Binary("libc6")
	bin.depsResolved = true

	debBlob, _ := store.Blobs.Store(strings.NewReader("libc6 deb"))
	ctlBlob := storeControl(t, store, "Package: libc6\nVersion: 2.40-1\nArchitecture: amd64\n")
	outs := []artifact.Output{{Name: "libc6.deb", Hash: debBlob}, {Name: "control:libc6", Hash: ctlBlob}}
	// Source build and binary validation both need manifests+SBOMs so the rootfs
	// can resolve the binary's producer and the binary's SBOM exists.
	storeManifestAndSBOM(t, store, src, outs, nil)
	binManifest, _ := storeManifestAndSBOM(t, store, bin, outs, map[string]objstore.Hash{"libc6.deb": debBlob, "control:libc6": ctlBlob})

	rootfs := newRootfsDirect("base", "amd64", []*debianBinaryPkg{bin}, store)
	rootfsTar, _ := store.Blobs.Store(strings.NewReader("rootfs tar bytes"))
	rootfsOuts := []artifact.Output{{Name: "rootfs.tar.gz", Hash: rootfsTar}}
	rootfsInputs := map[string]objstore.Hash{"libc6.deb": debBlob, "control:libc6": ctlBlob}
	_, rfsDoc := storeManifestAndSBOM(t, store, rootfs, rootfsOuts, rootfsInputs)

	rg := parseSPDX(t, rfsDoc)
	binDebURI := "urn:spdx:localhost/glbx/sboms/" + binManifest.String() + "/outputs/libc6.deb"
	payload := "urn:spdx:localhost/glbx/sboms/" + rg.resultManifest(t) + "/payload"
	if !rg.hasRel(payload, "contains", binDebURI) {
		t.Errorf("rootfs payload does not contain binary output %s; rels=%v", binDebURI, rg.relationships())
	}
	// And the rootfs.tar.gz expandsTo the payload.
	tarURI := "urn:spdx:localhost/glbx/sboms/" + rg.resultManifest(t) + "/outputs/rootfs.tar.gz"
	if !rg.hasRel(tarURI, "expandsTo", payload) {
		t.Errorf("rootfs tar does not expandTo payload")
	}
}

// TestSBOMImageContainsIncludedImage builds two images where one includes the
// other (disk includes initrd) and asserts the including image's output contains
// the included image's output element by a cross-document URI.
func TestSBOMImageContainsIncludedImage(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()

	// Minimal image type dirs with a build script and conf.yml so recipe hashing
	// finds files.
	mkType := func(name string) string {
		dir := filepath.Join(root, "images.d", name)
		mustWrite(t, filepath.Join(dir, "build"), "#!/bin/sh\n")
		mustWrite(t, filepath.Join(dir, "conf.yml"), "type: "+name+"\n")
		return dir
	}

	// A shared rootfs stub with a planted manifest+SBOM.
	srcDir := filepath.Join(root, "pkgs", "base-files")
	writeSourcePkg(t, srcDir, "Source: base-files\nPackage: base-files\n", "")
	src := NewDebianPkgBuild(DebianPkgBuildConfig{Name: "base-files", PkgDir: srcDir, Arch: "amd64", Store: store})
	bin := src.Binary("base-files")
	bin.depsResolved = true
	debBlob, _ := store.Blobs.Store(strings.NewReader("bf deb"))
	ctlBlob := storeControl(t, store, "Package: base-files\nVersion: 1\nArchitecture: amd64\n")
	bfOuts := []artifact.Output{{Name: "base-files.deb", Hash: debBlob}, {Name: "control:base-files", Hash: ctlBlob}}
	storeManifestAndSBOM(t, store, src, bfOuts, nil)
	storeManifestAndSBOM(t, store, bin, bfOuts, map[string]objstore.Hash{"base-files.deb": debBlob, "control:base-files": ctlBlob})

	rootfs := newRootfsDirect("base", "amd64", []*debianBinaryPkg{bin}, store)
	rootfsTar, _ := store.Blobs.Store(strings.NewReader("rootfs tar"))
	storeManifestAndSBOM(t, store, rootfs, []artifact.Output{{Name: "rootfs.tar.gz", Hash: rootfsTar}}, map[string]objstore.Hash{"base-files.deb": debBlob, "control:base-files": ctlBlob})

	// initrd image.
	initrd := newImage(ImageConfig{Name: "initrd", Arch: "amd64", TypeDir: mkType("initrd"), Rootfs: rootfs, BaseDir: root, Store: store})
	initrdBlob, _ := store.Blobs.Store(strings.NewReader("initrd image bytes"))
	initrdManifest, _ := storeManifestAndSBOM(t, store, initrd, []artifact.Output{{Name: imageOutputName, Hash: initrdBlob}}, map[string]objstore.Hash{"rootfs.tar.gz": rootfsTar})

	// disk image includes initrd.
	disk := newImage(ImageConfig{Name: "disk", Arch: "amd64", TypeDir: mkType("disk"), Rootfs: rootfs, BaseDir: root, Store: store})
	disk.setInputs([]imageInput{{label: "initrd", imageDep: initrd}})
	diskBlob, _ := store.Blobs.Store(strings.NewReader("disk image bytes"))
	_, diskDoc := storeManifestAndSBOM(t, store, disk, []artifact.Output{{Name: imageOutputName, Hash: diskBlob}}, map[string]objstore.Hash{"rootfs.tar.gz": rootfsTar, imageOutputName: initrdBlob})

	dg := parseSPDX(t, diskDoc)
	diskImgURI := "urn:spdx:localhost/glbx/sboms/" + dg.resultManifest(t) + "/outputs/" + segEscape(imageOutputName)
	initrdImgURI := "urn:spdx:localhost/glbx/sboms/" + initrdManifest.String() + "/outputs/" + segEscape(imageOutputName)
	if !dg.hasRel(diskImgURI, "contains", initrdImgURI) {
		t.Errorf("disk image does not contain included initrd image %s; rels=%v", initrdImgURI, dg.relationships())
	}
}
