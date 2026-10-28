// Package provider makes import and lockfile generation pluggable operations.
// Each operation is implemented by a provider registered under a namespace
// string ("debian", "rootfs", "pkg", "debian-kernel"); the command layer parses
// a <namespace>:<arg> target, selects the provider, and hands it an OpContext
// and an opaque, provider-validated parameter set.
//
// Unlike an artifact build — which reads cache blobs and writes cache blobs — an
// import or lockfile operation reads and writes the configuration tree
// (pkgs/<name>/..., top-level *-deps.yml) as well as the object-store cache.
// That dual nature is carried on OpContext.
//
// Providers that only run glbx's own code (every importer, and the plain Debian
// and rootfs lock generators) run directly. A provider that must run a package's
// own untrusted tooling (the Debian-kernel control regeneration) constructs a
// container chroot from resolved object-store blobs and runs inside it; whether
// it does so is hidden behind the provider interface.
package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gardenlinux/glbx/internal/objstore"
)

// OpContext is the ambient environment handed to every provider: the
// logger-bearing context, the object store (cache), the working-tree root whose
// pkgs/<name>/ and top-level *-deps.yml files the provider reads and writes, and
// the validated parameter set. StubPath locates the exec_env_stub binary a
// container-backed provider needs; providers that run no container ignore it.
type OpContext struct {
	Ctx       context.Context
	Store     *objstore.Store
	OutputDir string // working-tree root
	Params    Params
	StubPath  string
}

// Importer fetches an upstream package's pristine source into the working tree
// as pkgs/<name>/{sources.yml,src,build.yml?} plus the object-store blobs its
// sources reference, and records the import in the package's lineage.
//
// An Importer runs glbx's own code only; it needs no container.
//
// Import has no dispatch-by-config forwarder: it creates pkgs/<name>/ and so
// runs before any build.yml exists (and build.yml is optional besides). The
// namespace is therefore always explicit on the command line — "debian:<name>",
// never "pkg:<name>".
type Importer interface {
	// Namespace is the stable selector, e.g. "debian".
	Namespace() string

	// ParamsHelp is the free-form help string printed by
	// `glbx import --params-help <namespace>`.
	ParamsHelp() string

	// Import imports the named package. It returns a structured result for
	// --no-merge mode and machine consumers.
	Import(op OpContext, name string, opts ImportOptions) (*ImportResult, error)
}

// ImportOptions carries the import-shaped flags that are not provider-specific
// (they apply to any import namespace), kept off the opaque Params channel.
type ImportOptions struct {
	GitHistory bool   // record an import commit on the package lineage
	Merge      bool   // merge the import commit into the current branch
	UpdateTag  string // auto-update source recorded in the import commit
}

// ImportResult is the structured outcome of an import. The command layer
// branches on it to decide whether to run a git merge
// (Committed/AlreadyPresent/FirstImport) and, in --no-merge mode, serializes it
// as JSON for the update-PR pipeline to consume.
type ImportResult struct {
	Name    string
	Version string
	Format  string
	Sources []ImportedSource

	// Committed is true when the import was recorded as a git commit. CommitHash
	// is that commit, FirstImport reports whether it is the orphan root of the
	// package lineage (merged with --allow-unrelated-histories), and
	// AlreadyPresent is true when a prior import already pinned this exact
	// version, so nothing was imported.
	Committed      bool
	CommitHash     string
	FirstImport    bool
	AlreadyPresent bool
}

// ImportedSource is one upstream archive stored during an import: its filename
// and the content hash that pins it.
type ImportedSource struct {
	Name string
	Hash string
}

// LockGenerator resolves the exact set of external packages a build step needs,
// fetches them into the cache, and writes the pinned lock file into the working
// tree (pkgs/<name>/build-deps.yml, or top-level rootfs-deps.yml).
//
// A LockGenerator may need a container (the Debian-kernel provider regenerates
// debian/control by running the package's own build tooling); most do not.
// Whether it does is an implementation detail hidden behind Generate.
//
// Generate returns only an error. A lock's outcome (which file was written, how
// many packages) is a log line the provider emits through op.Ctx, and anything
// a machine needs is readable from the written lock file on disk.
type LockGenerator interface {
	// Namespace is the stable selector, e.g. "debian", "debian-kernel",
	// "rootfs", "pkg".
	Namespace() string

	// ParamsHelp is the free-form help string printed by
	// `glbx lockfile --params-help <namespace>`. The pkg forwarder composes its
	// delegates' strings.
	ParamsHelp() string

	// Generate produces the lock for the given argument (a package name for
	// "pkg"/"debian"/"debian-kernel"; "" for "rootfs"), logging its own
	// completion through op.Ctx.
	Generate(op OpContext, arg string) error
}

// The installed providers, keyed by namespace.
var (
	importers      = map[string]Importer{}
	lockGenerators = map[string]LockGenerator{}
)

// RegisterImporter installs an Importer under its namespace.
func RegisterImporter(i Importer) { importers[i.Namespace()] = i }

// RegisterLockGenerator installs a LockGenerator under its namespace.
func RegisterLockGenerator(g LockGenerator) { lockGenerators[g.Namespace()] = g }

// LookupImporter returns the importer registered for a namespace.
func LookupImporter(ns string) (Importer, error) {
	i, ok := importers[ns]
	if !ok {
		return nil, fmt.Errorf("unknown import namespace %q (have: %s)", ns, knownImporters())
	}
	return i, nil
}

// LookupLockGenerator returns the lock generator registered for a namespace.
func LookupLockGenerator(ns string) (LockGenerator, error) {
	g, ok := lockGenerators[ns]
	if !ok {
		return nil, fmt.Errorf("unknown lockfile namespace %q (have: %s)", ns, knownLockGenerators())
	}
	return g, nil
}

func knownImporters() string {
	var names []string
	for ns := range importers {
		names = append(names, ns)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func knownLockGenerators() string {
	var names []string
	for ns := range lockGenerators {
		names = append(names, ns)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
