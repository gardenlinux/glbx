package build

import (
	"os"
	"path/filepath"
	"testing"
)

// writeMinimalPkg writes a source package tree (control) under baseDir/<name>/.
func writeMinimalPkg(t *testing.T, baseDir, name, control string) {
	t.Helper()
	dir := filepath.Join(baseDir, name)
	if err := os.MkdirAll(filepath.Join(dir, "src", "debian"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "debian", "control"), []byte(control), 0644); err != nil {
		t.Fatal(err)
	}
}

// A tooling build_depends resolves only within the ToolingSet: a name present
// only in pkgs/ is unresolvable from tooling, and a tooling source is not
// reachable from the shipped PackageSet.
func TestToolingDepsStayWithinUniverse(t *testing.T) {
	store := newStore(t)
	root := t.TempDir()
	toolingDir := filepath.Join(root, "images.d", "tooling")

	// Two tooling packages: libtool-helper and a consumer that build-depends on
	// it. A same-named shipped build_depends target (gcc) exists only in pkgs/.
	writeMinimalPkg(t, toolingDir, "libtool-helper", "Source: libtool-helper\nPackage: libtool-helper\nArchitecture: any\n")
	writeMinimalPkg(t, toolingDir, "consumer", "Source: consumer\nPackage: consumer\nArchitecture: any\n")
	// consumer's build.yml: build_depends on the tooling libtool-helper.
	if err := os.WriteFile(filepath.Join(toolingDir, "consumer", "build.yml"),
		[]byte("pkg_type: debian\nbuild_depends:\n  - libtool-helper:libtool-helper\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ts, err := NewToolingSet(toolingDir, "amd64", store, "", "")
	if err != nil {
		t.Fatalf("NewToolingSet: %v", err)
	}

	consumer := ts.Build("consumer")
	if consumer == nil {
		t.Fatal("consumer tooling build not found")
	}
	deps := consumer.Depends()
	if len(deps) != 1 || deps[0].Key() != "tooling-pkg-build:libtool-helper:amd64" {
		t.Fatalf("consumer deps = %v, want the libtool-helper tooling build", deps)
	}

	// A tooling build_depends on a name absent from the tooling universe is a
	// hard error — it cannot reach into pkgs/.
	writeMinimalPkg(t, toolingDir, "bad", "Source: bad\nPackage: bad\nArchitecture: any\n")
	if err := os.WriteFile(filepath.Join(toolingDir, "bad", "build.yml"),
		[]byte("pkg_type: debian\nbuild_depends:\n  - gcc:gcc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewToolingSet(toolingDir, "amd64", store, "", ""); err == nil {
		t.Fatal("a tooling build_depends on a non-tooling source should be rejected")
	}
}

// With an images.yml present, BuildGraph produces image:* nodes each depending
// on the right rootfs and the tooling-pkg-build nodes of their local_tooling.
func TestBuildGraphWiresImages(t *testing.T) {
	store := newStore(t)
	confDir := t.TempDir()

	// One shipped package and one rootfs variant referencing it.
	pkgsDir := filepath.Join(confDir, "pkgs")
	writeMinimalPkg(t, pkgsDir, "glbxtest", "Source: glbxtest\nPackage: glbxtest\nArchitecture: all\n")
	if err := os.WriteFile(filepath.Join(confDir, "rootfs-base.yml"), []byte("packages:\n  - glbxtest:glbxtest\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// One tooling package and an image type using it as local_tooling.
	toolingDir := filepath.Join(confDir, "images.d", "tooling")
	writeMinimalPkg(t, toolingDir, "mktool", "Source: mktool\nPackage: mktool\nArchitecture: any\n")
	if err := os.WriteFile(filepath.Join(toolingDir, "mktool", "build.yml"), []byte("pkg_type: debian\n"), 0644); err != nil {
		t.Fatal(err)
	}
	typeDir := filepath.Join(confDir, "images.d", "plain-ext4")
	if err := os.MkdirAll(typeDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(typeDir, "conf.yml"), []byte("debian_tooling: [e2fsprogs]\nlocal_tooling:\n  - mktool:mktool\n"), 0644)
	os.WriteFile(filepath.Join(typeDir, "build"), []byte("#!/bin/sh\n"), 0755)
	os.WriteFile(filepath.Join(typeDir, "tooling-deps.yml"), []byte("tools: []\n"), 0644)
	os.WriteFile(filepath.Join(confDir, "images.yml"), []byte("images:\n  - name: base-ext4\n    rootfs: base\n    type: plain-ext4\n"), 0644)

	gr, err := BuildGraph(GraphConfig{ConfDir: confDir, Arch: "amd64", Store: store})
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if len(gr.Images) != 1 || gr.Images[0].Key() != "image:base-ext4:amd64" {
		t.Fatalf("images = %+v", gr.Images)
	}
	if gr.Graph.Find("image:base-ext4:amd64") == nil {
		t.Fatal("image node missing from graph")
	}
	if gr.Graph.Find("tooling-pkg-build:mktool:amd64") == nil {
		t.Fatal("local-tooling build node missing from graph")
	}
	// The image depends on its rootfs and the local-tooling build.
	deps := gr.Images[0].Depends()
	var hasRootfs, hasTooling bool
	for _, d := range deps {
		switch d.Key() {
		case "rootfs:base:amd64":
			hasRootfs = true
		case "tooling-pkg-build:mktool:amd64":
			hasTooling = true
		}
	}
	if !hasRootfs || !hasTooling {
		t.Fatalf("image deps = %v, want rootfs + tooling build", deps)
	}
}

// Without images.yml the graph is unchanged from the image-free shape: no image
// nodes, roots are the rootfs variants.
func TestBuildGraphNoImagesBackwardCompat(t *testing.T) {
	store := newStore(t)
	confDir := t.TempDir()
	pkgsDir := filepath.Join(confDir, "pkgs")
	writeMinimalPkg(t, pkgsDir, "glbxtest", "Source: glbxtest\nPackage: glbxtest\nArchitecture: all\n")
	if err := os.WriteFile(filepath.Join(confDir, "rootfs-base.yml"), []byte("packages:\n  - glbxtest:glbxtest\n"), 0644); err != nil {
		t.Fatal(err)
	}

	gr, err := BuildGraph(GraphConfig{ConfDir: confDir, Arch: "amd64", Store: store})
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	if len(gr.Images) != 0 {
		t.Fatalf("expected no images, got %+v", gr.Images)
	}
	for _, key := range gr.Graph.Keys() {
		if len(key) >= 6 && key[:6] == "image:" {
			t.Fatalf("unexpected image node %q in image-free graph", key)
		}
	}
}
