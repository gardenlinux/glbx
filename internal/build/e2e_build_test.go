package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/importer"
	"github.com/gardenlinux/glbx/internal/lockfile"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

func requireStubBuild(t *testing.T) string {
	t.Helper()
	stub := os.Getenv("GLBX_EXEC_ENV_STUB")
	if stub == "" {
		t.Skip("GLBX_EXEC_ENV_STUB not set; run `make test`")
	}
	if _, err := os.Stat(stub); err != nil {
		t.Skipf("GLBX_EXEC_ENV_STUB=%q does not exist", stub)
	}
	return stub
}

// TestBuildSinglePackageE2E imports a dependency-free source package, locks its
// build tooling, builds it from source inside the sandbox via the engine, and
// confirms the produced .deb lands in the store. Gated on the stub + network.
func TestBuildSinglePackageE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping single-package build e2e in -short mode")
	}
	stub := requireStubBuild(t)

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := log.WithTarget(context.Background(), log.Discard)
	work := t.TempDir()
	const pkg = "hello"
	arch := buildcfg.HostArch()
	cookie := "build-e2e"

	// import
	if _, err := importer.Import(importer.ImportConfig{
		Ctx: ctx, Store: store, OutputDir: work, Cookie: cookie,
	}, pkg); err != nil {
		t.Skipf("import (network required): %v", err)
	}
	// lock
	if _, err := lockfile.Generate(lockfile.Config{
		Ctx: ctx, Store: store, Arch: arch, OutputDir: work, PkgName: pkg, Cookie: cookie,
	}); err != nil {
		t.Skipf("lockfile (network required): %v", err)
	}

	sb := NewDebianPkgBuild(DebianPkgBuildConfig{
		Name:     pkg,
		PkgDir:   filepath.Join(work, "pkgs", pkg),
		Arch:     arch,
		Store:    store,
		StubPath: stub,
	})

	g, err := artifact.Discover([]artifact.Artifact{sb})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	eng := artifact.NewEngine(g, store, 1)
	if _, err := eng.Run(ctx); err != nil {
		t.Fatalf("build: %v", err)
	}

	// The source build's manifest must now resolve, and the hello binary .deb
	// must be present in the store.
	manifest, leaves, err := sb.OutputRefs(store)
	if err != nil {
		t.Fatalf("source build not recorded: %v", err)
	}
	if manifest.IsZero() || len(leaves) == 0 {
		t.Fatalf("no outputs recorded: manifest=%s leaves=%d", manifest, len(leaves))
	}
	if pkgEntry := sb.LoadBinaryPkg("hello", store); pkgEntry == nil {
		t.Fatal("hello binary not found in source-build manifest")
	}
}

// TestBinaryValidationE2E builds hello from source, then runs its binary
// artifact through locality + install-check validation. hello's only runtime
// dep (libc6) is not locally built here, so it is tolerated via lockfile_deps;
// the install check bootstraps a base system and installs hello into it.
func TestBinaryValidationE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary validation e2e in -short mode")
	}
	stub := requireStubBuild(t)

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := log.WithTarget(context.Background(), log.Discard)
	work := t.TempDir()
	const pkg = "hello"
	arch := buildcfg.HostArch()
	cookie := "binval-e2e"

	if _, err := importer.Import(importer.ImportConfig{Ctx: ctx, Store: store, OutputDir: work, Cookie: cookie}, pkg); err != nil {
		t.Skipf("import (network required): %v", err)
	}
	if _, err := lockfile.Generate(lockfile.Config{Ctx: ctx, Store: store, Arch: arch, OutputDir: work, PkgName: pkg, Cookie: cookie}); err != nil {
		t.Skipf("lockfile (network required): %v", err)
	}

	// Tolerate hello's external runtime dep (libc6) during validation.
	pkgDir := filepath.Join(work, "pkgs", pkg)
	if err := os.WriteFile(filepath.Join(pkgDir, "build.yml"),
		[]byte("lockfile_deps:\n  hello: [libc6]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sb := NewDebianPkgBuild(DebianPkgBuildConfig{Name: pkg, PkgDir: pkgDir, Arch: arch, Store: store, StubPath: stub})
	bin := sb.Binary("hello")

	g, err := artifact.Discover([]artifact.Artifact{bin})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	eng := artifact.NewEngine(g, store, 1)
	results, err := eng.Run(ctx)
	if err != nil {
		t.Fatalf("binary validation build: %v", err)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("%s failed: %v", r.Artifact, r.Err)
		}
	}

	if _, _, err := bin.OutputRefs(store); err != nil {
		t.Fatalf("validated binary not recorded: %v", err)
	}
}
