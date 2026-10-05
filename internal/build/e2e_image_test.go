package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/lockfile"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// writeTrivialPackage authors a dependency-free, architecture-independent
// source package under pkgs/<name> whose build produces a .deb shipping a
// single data file. No upstream archive is needed (native-style tree).
func writeTrivialPackage(t *testing.T, pkgsDir, name string) {
	t.Helper()
	deb := filepath.Join(pkgsDir, name, "src", "debian")
	if err := os.MkdirAll(filepath.Join(deb, "source"), 0755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(deb, rel), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("source/format", "3.0 (native)\n", 0644)
	write("changelog", name+" (1.0) UNRELEASED; urgency=medium\n\n  * Initial.\n\n -- Dev <dev@localhost>  Mon, 01 Jan 2024 00:00:00 +0000\n", 0644)
	write("control", "Source: "+name+"\nSection: misc\nPriority: optional\nMaintainer: Dev <dev@localhost>\nStandards-Version: 4.6.2\nBuild-Depends: debhelper-compat (= 13)\n\nPackage: "+name+"\nArchitecture: all\nDescription: trivial test package\n", 0644)
	write("rules", "#!/usr/bin/make -f\n%:\n\tdh $@\n\noverride_dh_auto_install:\n\tmkdir -p debian/"+name+"/usr/share/"+name+"\n\techo marker > debian/"+name+"/usr/share/"+name+"/marker\n", 0755)
}

// TestImageBuildE2E authors a dependency-free package, locks its build tooling
// and the image configuration tooling, and assembles a rootfs image through the
// full engine — compile, validate, three-layer overlay, dpkg configure, pack —
// confirming a rootfs.tar.gz is produced. Gated on the stub + network.
func TestImageBuildE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping image build e2e in -short mode")
	}
	stub := requireStubBuild(t)

	store, err := objstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := log.WithTarget(context.Background(), log.Discard)
	work := t.TempDir()
	arch := buildcfg.HostArch()
	cookie := "image-e2e"

	writeTrivialPackage(t, filepath.Join(work, "pkgs"), "glbxtest")

	if _, err := lockfile.Generate(lockfile.Config{Ctx: ctx, Store: store, Arch: arch, OutputDir: work, PkgName: "glbxtest", Cookie: cookie}); err != nil {
		t.Skipf("lockfile (network required): %v", err)
	}

	os.WriteFile(filepath.Join(work, "rootfs.yml"), []byte("packages:\n  - glbxtest:glbxtest\n"), 0644)
	if _, err := lockfile.GenerateRootfs(lockfile.RootfsConfig{Ctx: ctx, Store: store, Arch: arch, OutputDir: work, Cookie: cookie}); err != nil {
		t.Skipf("rootfs lockfile (network required): %v", err)
	}

	gr, err := BuildGraph(GraphConfig{ConfDir: work, Arch: arch, Store: store, StubPath: stub})
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	eng := artifact.NewEngine(gr.Graph, store, 2)
	results, err := eng.Run(ctx)
	if err != nil {
		t.Fatalf("run engine: %v", err)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("%s failed: %v", r.Artifact, r.Err)
		}
	}

	manifest, leaves, err := gr.Rootfs.OutputRefs(store)
	if err != nil {
		t.Fatalf("rootfs not recorded: %v", err)
	}
	if manifest.IsZero() || len(leaves) == 0 {
		t.Fatalf("no image outputs: manifest=%s leaves=%d", manifest, len(leaves))
	}
}
