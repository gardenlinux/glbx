package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// This file is the shared, SPDX-opinionated helper library that every artifact's
// SBOM writer builds on. A writer works in Go-native terms — "this build has
// these inputs, these tools, these outputs, these relationships" — and this layer
// turns that into the concrete SPDX 3.0.1 JSON-LD document described in
// doc/src/concepts/sbom.md. The helpers are deliberately not a general SPDX
// abstraction: they encode exactly the glbx element shapes, ID scheme and
// profiles that the concept chapter fixes.
//
// The one invariant everything leans on: an element's SPDX ID is a pure function
// of (namespace, output-manifest hash, element path). That is what makes a
// consumer document reference a producer's elements with byte-identical URIs
// without the two ever coordinating — see spdxID and the cross-document import
// helpers below.

const (
	spdxContext    = "https://spdx.org/rdf/3.0.1/spdx-context.jsonld"
	spdxSpecVer    = "3.0.1"
	spdxEpoch      = "1970-01-01T00:00:00Z"
	spdxAgentName  = "glbx"
	buildTypeNS    = "urn:spdx:gardenlinux.io/glbx/build-types/"
	glbxVendor     = "gardenlinux"
	debVendor      = "debian"
	spdxMediaType  = "application/spdx+json"
	provenancePurp = "manifest"
)

// spdxDoc is a document under construction for one artifact result. A writer adds
// elements and relationships to it, then calls marshal to get the serialized
// bytes. The prefix (namespace + output-manifest hash) is fixed at construction
// and every local element ID hangs off it.
type spdxDoc struct {
	namespace string
	manifest  objstore.Hash // M: this result's output-manifest hash
	rootElem  string        // element path of the software_Package result root

	// graph accumulates every emitted element object in insertion order. localIDs
	// tracks which IDs are locally defined (listed in SpdxDocument.element and the
	// sbom), imports tracks foreign ExternalMap records and the documents/<N>
	// files that pin them.
	graph    []map[string]any
	localIDs map[string]bool
	relIDs   map[string]bool // dedup identical relationships

	// importDocs maps a producer manifest hash N -> the documents/<N> file element
	// ID in this graph, so repeated references to one producer pin it once.
	importDocs map[string]string
	// externalMaps accumulates the SpdxDocument.import ExternalMap records.
	externalMaps []map[string]any
	// foreignIDs are referenced producer element IDs that are NOT local; they must
	// not be added to SpdxDocument.element / sbom.element.
	foreignIDs map[string]bool
}

// newSPDXDoc starts a document for a result in a namespace, keyed by its
// output-manifest hash. rootPath is the element path of the result package.
func newSPDXDoc(namespace string, manifest objstore.Hash) *spdxDoc {
	return &spdxDoc{
		namespace:  namespace,
		manifest:   manifest,
		localIDs:   map[string]bool{},
		relIDs:     map[string]bool{},
		importDocs: map[string]string{},
		foreignIDs: map[string]bool{},
	}
}

// prefix is urn:spdx:<A>/glbx/sboms/<M>. Element IDs append "/<path>".
func prefixFor(namespace string, manifest objstore.Hash) string {
	return "urn:spdx:" + namespace + "/glbx/sboms/" + manifest.String()
}

func (d *spdxDoc) prefix() string { return prefixFor(d.namespace, d.manifest) }

// spdxID mints a local element ID from an element path (e.g. "outputs/fixture.deb"
// or "build"). Path segments that carry arbitrary bytes (output names, file paths)
// must already be escaped by the caller via segEscape.
func (d *spdxDoc) spdxID(path string) string { return d.prefix() + "/" + path }

// foreignID mints the ID a producer with manifest hash N would have minted for an
// element path, in this document's namespace. This is the whole basis of
// cross-document consistency: consumer and producer compute the identical URI.
func (d *spdxDoc) foreignID(producer objstore.Hash, path string) string {
	return prefixFor(d.namespace, producer) + "/" + path
}

// segEscape percent-encodes a single path segment so names like "control:fixture"
// or "pkgs/fixture/build.yml" survive as one identifiable URI segment, matching
// the worked example (control%3Afixture, pkgs%2Ffixture%2Fbuild.yml).
func segEscape(s string) string {
	// url.PathEscape leaves '/' and ':' alone; the concept doc escapes both, so
	// do it explicitly and consistently.
	esc := url.PathEscape(s)
	esc = strings.ReplaceAll(esc, "/", "%2F")
	esc = strings.ReplaceAll(esc, ":", "%3A")
	return esc
}

// --- element constructors -------------------------------------------------
//
// Each returns the element's SPDX ID and appends the object to the graph,
// registering it as a local element. The ID is the handle a writer uses to wire
// relationships.

// creationInfoID is the shared CreationInfo element ID.
func (d *spdxDoc) creationInfoID() string { return d.spdxID("creation") }

// addScaffolding adds the elements every document has: the SoftwareAgent, the
// CreationInfo, the SpdxDocument and the software_Sbom are emitted by finalize;
// here we add the agent so relationships (invokedBy) can target it.
func (d *spdxDoc) addAgent() string {
	id := d.spdxID("agent")
	d.add(map[string]any{
		"type":         "SoftwareAgent",
		"spdxId":       id,
		"name":         spdxAgentName,
		"creationInfo": d.creationInfoID(),
	})
	return id
}

// addArtifactBuild adds the single build_Build element that is both the build
// process and the SBOM/document root (SPDX forbids one element being both a
// Build and a software_Package, so the Build — a Core Element — carries the
// result name directly). It emits the build type, the arch parameter, the agent
// invokedBy edge, the output-manifest software_File, and the hasMetadata edge to
// it. name is the build target key (e.g. debian-pkg-build:openssl:arm64). The
// returned ID is the root element.
func (d *spdxDoc) addArtifactBuild(kind, arch, name, agentID string) string {
	id := d.spdxID("artifact-build")
	d.add(map[string]any{
		"type":            "build_Build",
		"spdxId":          id,
		"name":            name,
		"creationInfo":    d.creationInfoID(),
		"build_buildType": buildTypeNS + kind,
		"build_parameter": []map[string]any{
			{"type": "DictionaryEntry", "key": "arch", "value": arch},
		},
	})
	d.rootElem = id
	d.relate(id, "invokedBy", agentID, "build")

	manID := d.spdxID("manifest")
	d.add(map[string]any{
		"type":                    "software_File",
		"spdxId":                  manID,
		"name":                    "glbx output manifest",
		"verifiedUsing":           hashBlock(d.manifest),
		"creationInfo":            d.creationInfoID(),
		"software_primaryPurpose": provenancePurp,
	})
	d.relate(id, "hasMetadata", manID, "")
	return id
}

// debPackage adds a software_Package for a Debian binary .deb: name, version,
// architecture-bearing PURL (vendor-selectable), and the archive checksum. path
// is the element path (already escaped where needed). Returns the element ID.
func (d *spdxDoc) debPackage(path, name, version, arch, vendor string, deb objstore.Hash) string {
	id := d.spdxID(path)
	d.add(map[string]any{
		"type":                    "software_Package",
		"spdxId":                  id,
		"name":                    name,
		"verifiedUsing":           hashBlock(deb),
		"creationInfo":            d.creationInfoID(),
		"software_packageUrl":     purl(vendor, name, version, arch),
		"software_packageVersion": version,
	})
	return id
}

// sourcePackage adds a source-purpose software_Package (arch=source PURL).
func (d *spdxDoc) sourcePackage(path, name, version, vendor string) string {
	id := d.spdxID(path)
	d.add(map[string]any{
		"type":                    "software_Package",
		"spdxId":                  id,
		"name":                    name,
		"creationInfo":            d.creationInfoID(),
		"software_packageUrl":     purl(vendor, name, version, "source"),
		"software_packageVersion": version,
		"software_primaryPurpose": "source",
	})
	return id
}

// fileElement adds a software_File identified by name and a byte checksum.
func (d *spdxDoc) fileElement(path, name string, hash objstore.Hash) string {
	id := d.spdxID(path)
	d.add(map[string]any{
		"type":          "software_File",
		"spdxId":        id,
		"name":          name,
		"verifiedUsing": hashBlock(hash),
		"creationInfo":  d.creationInfoID(),
	})
	return id
}

// plainFile adds a software_File with a checksum and an explicit primaryPurpose
// (used for the manifest and for rootfs/image file outputs when desired).
func (d *spdxDoc) outputFile(path, name string, hash objstore.Hash) string {
	return d.fileElement(path, name, hash)
}

// gitPackagingElement adds the source-purpose software_Package describing a
// Debian packaging directory by a VCS externalRef (git+<repo>@<commit>#<subdir>).
// The @<commit> and #<subdir> parts are each omitted when empty, so a non-git
// fallback (no commit) still yields a well-formed locator.
func (d *spdxDoc) gitPackagingElement(path, name, repo, commit, subdir string) string {
	id := d.spdxID(path)
	locator := "git+" + repo
	if commit != "" {
		locator += "@" + commit
	}
	if subdir != "" {
		locator += "#" + subdir
	}
	d.add(map[string]any{
		"type":                    "software_Package",
		"spdxId":                  id,
		"name":                    name,
		"creationInfo":            d.creationInfoID(),
		"software_primaryPurpose": "source",
		"externalRef": []map[string]any{
			{"type": "ExternalRef", "externalRefType": "vcs", "locator": []string{locator}},
		},
	})
	return id
}

// environment adds a software_Package that groups the packages present in one
// build environment. role is the element-path segment (e.g.
// "build-container-rootfs", "bootstrap", "layer1", "assembly"); name is the
// display label; purpose, when non-empty, sets software_primaryPurpose (e.g.
// "container"). Returns the element ID; the writer wires contains edges to
// members.
func (d *spdxDoc) environment(role, name, purpose string) string {
	id := d.spdxID("environment/" + role)
	obj := map[string]any{
		"type":         "software_Package",
		"spdxId":       id,
		"name":         name,
		"creationInfo": d.creationInfoID(),
	}
	if purpose != "" {
		obj["software_primaryPurpose"] = purpose
	}
	d.add(obj)
	return id
}

// payload adds the logical-filesystem software_Package a rootfs archive expands
// to, tagged with the filesystemImage purpose.
func (d *spdxDoc) payload(name string) string {
	id := d.spdxID("payload")
	d.add(map[string]any{
		"type":                    "software_Package",
		"spdxId":                  id,
		"name":                    name,
		"creationInfo":            d.creationInfoID(),
		"software_primaryPurpose": "filesystemImage",
	})
	return id
}

// --- relationships --------------------------------------------------------

// relate records a relationship from -> to. scope != "" makes it a
// LifecycleScopedRelationship with that scope (build-scoped facts); scope == ""
// makes it a plain Relationship (byte copies, containment, hasMetadata). The
// relationship's own ID is folded from its fields so identical edges dedup and the
// ID is reproducible. A foreign `from` or `to` is allowed; it is recorded as such.
func (d *spdxDoc) relate(from, relType, to, scope string) {
	comment := "" // reserved; no edge uses a comment today
	key := strings.Join([]string{from, relType, scope, to, comment}, "\x00")
	h := sha256.Sum256([]byte(key))
	relHash := hex.EncodeToString(h[:])
	id := d.spdxID("relationships/" + relHash)
	if d.relIDs[relHash] {
		return
	}
	d.relIDs[relHash] = true

	obj := map[string]any{
		"spdxId":           id,
		"relationshipType": relType,
		"from":             from,
		"to":               []string{to},
		"creationInfo":     d.creationInfoID(),
	}
	if scope != "" {
		obj["type"] = "LifecycleScopedRelationship"
		obj["scope"] = scope
	} else {
		obj["type"] = "Relationship"
	}
	// Insert relationships after element bodies; just append — order is normalized
	// at marshal time by sorting element arrays, and @graph order is cosmetic.
	d.graph = append(d.graph, obj)
	d.localIDs[id] = true
}

// --- cross-document imports ----------------------------------------------

// importElement declares that this document uses a foreign element minted by the
// producer with manifest hash N at element path `path`. It:
//   - ensures a documents/<N> software_File exists pinning the producer's whole
//     serialized SPDX document by checksum (docChecksum) and file name,
//   - adds an ExternalMap record naming the foreign element and that defining file,
//   - returns the foreign element's full ID so the caller can target it in edges.
//
// The foreign ID is NOT added to the local element list.
//
// found=false means the producer's SPDX document checksum is undefined (its SBOM is
// not resolvable in this namespace — pulled from cache without its map entry, or
// living only under another namespace). docChecksum is ignored. The foreign element
// URN is a pure function of (namespace, N, path) and is still minted, so every edge
// wires unchanged; only the byte pin is dropped: no documents/<N> defining-file
// element is emitted, and the ExternalMap carries neither definingArtifact nor
// locationHint but a comment marking the document unavailable.
func (d *spdxDoc) importElement(producer objstore.Hash, path string, docChecksum objstore.Hash, found bool) string {
	foreign := d.foreignID(producer, path)
	d.foreignIDs[foreign] = true

	if !found {
		d.externalMaps = append(d.externalMaps, map[string]any{
			"type":           "ExternalMap",
			"externalSpdxId": foreign,
			"comment":        "glbx: referenced SBOM document unavailable in namespace " + strconv.Quote(d.namespace) + "; document checksum undefined",
		})
		return foreign
	}

	defID, ok := d.importDocs[producer.String()]
	if !ok {
		defID = d.spdxID("documents/" + producer.String())
		d.importDocs[producer.String()] = defID
		d.add(map[string]any{
			"type":          "software_File",
			"spdxId":        defID,
			"name":          producer.String() + ".spdx.json",
			"contentType":   spdxMediaType,
			"verifiedUsing": hashBlock(docChecksum),
			"creationInfo":  d.creationInfoID(),
		})
	}
	d.externalMaps = append(d.externalMaps, map[string]any{
		"type":             "ExternalMap",
		"externalSpdxId":   foreign,
		"definingArtifact": defID,
		"locationHint":     producer.String() + ".spdx.json",
	})
	return foreign
}

// --- assembly -------------------------------------------------------------

// add appends an element object and registers its ID as local.
func (d *spdxDoc) add(obj map[string]any) {
	if id, ok := obj["spdxId"].(string); ok {
		d.localIDs[id] = true
	}
	d.graph = append(d.graph, obj)
}

// marshal finalizes the document: it synthesizes the CreationInfo, the
// software_Sbom, and the SpdxDocument (with element and import lists), then
// serializes the whole @graph deterministically. It must be called once, last.
func (d *spdxDoc) marshal() ([]byte, error) {
	if d.rootElem == "" {
		return nil, fmt.Errorf("spdx document has no result root element")
	}
	agentID := d.spdxID("agent")

	// CreationInfo (uses @id, not spdxId, per the worked example).
	d.graph = append(d.graph, map[string]any{
		"type":        "CreationInfo",
		"@id":         d.creationInfoID(),
		"created":     spdxEpoch,
		"createdBy":   []string{agentID},
		"specVersion": spdxSpecVer,
	})

	// Collect the local element IDs (everything registered, minus the document
	// itself which is not listed in its own element array, minus the CreationInfo
	// which is referenced via creationInfo not element).
	var localElems []string
	for id := range d.localIDs {
		if id == d.spdxID("document") {
			continue
		}
		localElems = append(localElems, id)
	}
	sort.Strings(localElems)

	sbomID := d.spdxID("sbom")
	// The sbom groups all local elements except itself and the document.
	sbomElems := make([]string, 0, len(localElems))
	for _, id := range localElems {
		if id == sbomID {
			continue
		}
		sbomElems = append(sbomElems, id)
	}

	d.graph = append(d.graph, map[string]any{
		"type":              "software_Sbom",
		"spdxId":            sbomID,
		"rootElement":       []string{d.rootElem},
		"creationInfo":      d.creationInfoID(),
		"element":           sbomElems,
		"software_sbomType": []string{"build"},
	})
	// sbom is itself a local element listed in the document.
	d.localIDs[sbomID] = true
	localElems = append(localElems, sbomID)
	sort.Strings(localElems)

	docObj := map[string]any{
		"type":               "SpdxDocument",
		"spdxId":             d.spdxID("document"),
		"rootElement":        []string{sbomID},
		"creationInfo":       d.creationInfoID(),
		"element":            localElems,
		"profileConformance": []string{"build", "core", "software"},
	}
	if len(d.externalMaps) > 0 {
		docObj["import"] = d.externalMaps
	}
	d.graph = append(d.graph, docObj)

	out := map[string]any{
		"@context": spdxContext,
		"@graph":   d.graph,
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal spdx: %w", err)
	}
	b = append(b, '\n')
	return b, nil
}

// --- small value helpers --------------------------------------------------

func hashBlock(h objstore.Hash) []map[string]any {
	return []map[string]any{
		{"type": "Hash", "algorithm": "sha256", "hashValue": h.String()},
	}
}

func purl(vendor, name, version, arch string) string {
	return fmt.Sprintf("pkg:deb/%s/%s@%s?arch=%s", vendor, name, version, arch)
}

// producerManifest resolves a dependency artifact's output-manifest hash and the
// checksum of its SPDX document in this namespace, for cross-document imports. It
// returns the manifest hash N, the SPDX document checksum D, and found. D is needed
// to pin the producer document; it is the blob the producer's SBOM map entry points
// at.
//
// found=false (D undefined, no error) means the producer's SBOM is not resolvable
// in this namespace: it was pulled from the cache without its SBOM map entry, or it
// only exists under a different namespace. The manifest hash N is still returned so
// the caller can mint the foreign element URN; only the document checksum is absent.
// A hard error is returned only when the producer has no output manifest at all.
func producerManifest(a artifact.Artifact, store *objstore.Store, namespace string) (manifest, docChecksum objstore.Hash, found bool, err error) {
	m, _, err := a.OutputRefs(store)
	if err != nil {
		return objstore.Hash{}, objstore.Hash{}, false, fmt.Errorf("%s output refs: %w", a.Key(), err)
	}
	key := artifact.SBOMMapKey(namespace, m)
	dc, err := store.Map.Get(key)
	if err != nil {
		return m, objstore.Hash{}, false, nil
	}
	return m, dc, true, nil
}
