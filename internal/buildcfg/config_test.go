package buildcfg

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadBuildYML_Full(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "build.yml"), `pkg_type: debian-kernel
gen_control_pkgs: [debhelper, python3, quilt]
build_profiles: [nocheck, noudeb]
build_options: [nocheck]
extra_build_env:
  - "DEB_CFLAGS_APPEND=-Wno-error"
  - "OTHER=1"
build_depends: [glibc:libc6-dev, attr:libattr1-dev]
runtime_depends:
  libc6: [gcc-16:libgcc-s1, glibc:libc-gconv-modules-extra]
  libfoo: [bar:baz]
lockfile_deps:
  libc6: [linux-libc-dev, rpcsvc-proto]
`)
	cfg, err := LoadBuildYML(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PkgType != "debian-kernel" {
		t.Fatalf("PkgType: %q", cfg.PkgType)
	}
	if len(cfg.GenControlPkgs) != 3 || cfg.GenControlPkgs[2] != "quilt" {
		t.Fatalf("GenControlPkgs: %v", cfg.GenControlPkgs)
	}
	if len(cfg.BuildProfiles) != 2 || cfg.BuildProfiles[0] != "nocheck" {
		t.Fatalf("BuildProfiles: %v", cfg.BuildProfiles)
	}
	if len(cfg.BuildOptions) != 1 || cfg.BuildOptions[0] != "nocheck" {
		t.Fatalf("BuildOptions: %v", cfg.BuildOptions)
	}
	if len(cfg.ExtraBuildEnv) != 2 || cfg.ExtraBuildEnv[0] != "DEB_CFLAGS_APPEND=-Wno-error" {
		t.Fatalf("ExtraBuildEnv: %v", cfg.ExtraBuildEnv)
	}
	if len(cfg.BuildDepends) != 2 || cfg.BuildDepends[1] != "attr:libattr1-dev" {
		t.Fatalf("BuildDepends: %v", cfg.BuildDepends)
	}
	if len(cfg.RuntimeDepends["libc6"]) != 2 {
		t.Fatalf("RuntimeDepends.libc6: %v", cfg.RuntimeDepends["libc6"])
	}
	if len(cfg.LockfileDeps["libc6"]) != 2 || cfg.LockfileDeps["libc6"][0] != "linux-libc-dev" {
		t.Fatalf("LockfileDeps.libc6: %v", cfg.LockfileDeps["libc6"])
	}
}

func TestLoadBuildYML_Missing(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadBuildYML(dir)
	if err != nil {
		t.Fatalf("missing build.yml should not error: %v", err)
	}
	if len(cfg.BuildProfiles) != 0 || len(cfg.BuildDepends) != 0 {
		t.Fatalf("expected zero value for missing build.yml, got %+v", cfg)
	}
}

func TestLoadBuildYML_Malformed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "build.yml"), "{this is: not: valid yaml")
	if _, err := LoadBuildYML(dir); err == nil {
		t.Fatal("expected a parse error for malformed build.yml")
	}
}

func TestLoadSourcesYML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "sources.yml"), `sources:
  - file: "bash_5.3.orig.tar.xz"
    sha256: "a70de6bb41f5e192534a5a1836b1d7fad9a8d4818a6e1506d70f38441552c17a"
    urls:
      - "https://deb.debian.org/debian/pool/main/b/bash/bash_5.3.orig.tar.xz"
      - "https://snapshot.debian.org/.../bash_5.3.orig.tar.xz"
  - file: "bash_5.3.orig-doc.tar.xz"
    sha256: "fb81856a93eb6572d2c9e43c06a52c7cc17f47506dee98059ab90c8240444abb"
    urls:
      - "https://deb.debian.org/debian/pool/main/b/bash/bash_5.3.orig-doc.tar.xz"
`)
	archives, err := LoadSourcesYML(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(archives) != 2 {
		t.Fatalf("expected 2 archives, got %d", len(archives))
	}
	if archives[0].File != "bash_5.3.orig.tar.xz" {
		t.Fatalf("archive[0].File: %s", archives[0].File)
	}
	if archives[0].Hash.IsZero() {
		t.Fatalf("archive[0].Hash should be populated")
	}
	if len(archives[0].URLs) != 2 {
		t.Fatalf("archive[0].URLs: %v", archives[0].URLs)
	}
	if archives[1].File != "bash_5.3.orig-doc.tar.xz" {
		t.Fatalf("archive[1].File: %s", archives[1].File)
	}
}

func TestLoadSourcesYML_Missing(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSourcesYML(dir)
	if err == nil {
		t.Fatal("expected error for missing sources.yml")
	}
	if !os.IsNotExist(err) {
		t.Fatalf("missing sources.yml should report os.IsNotExist, got %v", err)
	}
}

func TestLoadSourcesYML_InvalidHashErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "sources.yml"), `sources:
  - file: "x.tar.xz"
    sha256: "not-a-real-hash"
`)
	if _, err := LoadSourcesYML(dir); err == nil {
		t.Fatal("expected error for an invalid hash in sources.yml")
	}
}

func TestLoadBuildDeps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build-deps.yml")
	writeFile(t, path, `tools:
  - name: gcc-14
    version: "14.2.0-3"
    files:
      - arch: amd64
        sha256: "100228bd70ee0f2d5a7889527d0718dbdb890335338b6491336aae418a2e666d"
        urls:
          - "https://deb.debian.org/debian/pool/main/g/gcc-14/gcc-14_amd64.deb"
      - arch: arm64
        sha256: "fb81856a93eb6572d2c9e43c06a52c7cc17f47506dee98059ab90c8240444abb"
        urls:
          - "https://deb.debian.org/debian/pool/main/g/gcc-14/gcc-14_arm64.deb"
  - name: debhelper
    version: "13.20"
    files:
      - arch: all
        sha256: "9a09690faf8b2b09cb02be917387a121be291af320548a1600b5105bc646be53"
        urls:
          - "https://deb.debian.org/debian/pool/main/d/debhelper/debhelper_all.deb"
`)
	tools, err := LoadBuildDeps(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "gcc-14" || tools[0].Version != "14.2.0-3" {
		t.Fatalf("tool[0]: %+v", tools[0])
	}
	if len(tools[0].Files) != 2 {
		t.Fatalf("tool[0] files: %v", tools[0].Files)
	}

	// FilesForArch picks the arch match plus any "all" file.
	amd := FilesForArch(tools, "amd64")
	if len(amd) != 2 { // gcc-14/amd64 + debhelper/all
		t.Fatalf("FilesForArch(amd64) = %d files, want 2", len(amd))
	}
	arm := FilesForArch(tools, "arm64")
	if len(arm) != 2 { // gcc-14/arm64 + debhelper/all
		t.Fatalf("FilesForArch(arm64) = %d files, want 2", len(arm))
	}
	riscv := FilesForArch(tools, "riscv64")
	if len(riscv) != 1 { // only debhelper/all
		t.Fatalf("FilesForArch(riscv64) = %d files, want 1 (the all file)", len(riscv))
	}
	if riscv[0].Arch != "all" {
		t.Fatalf("riscv match should be the all file, got %q", riscv[0].Arch)
	}
}

func TestLoadBuildDeps_MissingFile(t *testing.T) {
	if _, err := LoadBuildDeps(filepath.Join(t.TempDir(), "nope.yml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadBuildDeps_InvalidHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build-deps.yml")
	writeFile(t, path, `tools:
  - name: gcc-14
    version: "1"
    files:
      - arch: amd64
        sha256: "nope"
`)
	if _, err := LoadBuildDeps(path); err == nil {
		t.Fatal("expected error for invalid hash")
	}
}

func TestLoadRootfsLabel(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rootfs-base.yml"), `packages: [base-files:base-files, bash:bash, coreutils:coreutils]
`)
	pkgs := LoadRootfsLabel(dir, "base")
	if len(pkgs) != 3 || pkgs[0] != "base-files:base-files" || pkgs[2] != "coreutils:coreutils" {
		t.Fatalf("unexpected packages: %v", pkgs)
	}
}

func TestLoadRootfsLabel_BlockList(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rootfs-min.yml"), `packages:
  - base-files:base-files
  - bash:bash
`)
	pkgs := LoadRootfsLabel(dir, "min")
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d: %v", len(pkgs), pkgs)
	}
}

func TestLoadRootfsLabel_MissingTolerated(t *testing.T) {
	if pkgs := LoadRootfsLabel(t.TempDir(), "base"); pkgs != nil {
		t.Fatalf("expected nil on missing rootfs-base.yml, got %v", pkgs)
	}
}

func TestDiscoverRootfsLabels(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "rootfs-base.yml"), "packages: [bash:bash]\n")
	writeFile(t, filepath.Join(dir, "rootfs-min.yml"), "packages: [bash:bash]\n")
	// rootfs-deps.yml is the shared tooling lock, not a variant, and is excluded.
	writeFile(t, filepath.Join(dir, "rootfs-deps.yml"), "tools: []\n")
	labels := DiscoverRootfsLabels(dir)
	if len(labels) != 2 || labels[0] != "base" || labels[1] != "min" {
		t.Fatalf("unexpected labels (want [base min]): %v", labels)
	}
}

func TestHostArch(t *testing.T) {
	a := HostArch()
	if a == "" {
		t.Fatal("HostArch returned empty string")
	}
	// Cached: a second call returns the same value.
	if HostArch() != a {
		t.Fatal("HostArch not stable across calls")
	}
}

func TestGoArchToDebian(t *testing.T) {
	cases := map[string]string{
		"amd64":   "amd64",
		"arm64":   "arm64",
		"386":     "i386",
		"arm":     "armhf",
		"ppc64le": "ppc64el",
		"weird":   "weird",
	}
	for in, want := range cases {
		if got := goArchToDebian(in); got != want {
			t.Errorf("goArchToDebian(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateToolingBuildYML(t *testing.T) {
	// A bare build-only tooling package is accepted.
	ok := BuildYML{PkgType: "debian", BuildDepends: []string{"gcc:gcc"}}
	if err := ValidateToolingBuildYML(ok); err != nil {
		t.Errorf("bare tooling build.yml rejected: %v", err)
	}
	// runtime_depends, lockfile_deps, and signing are each forbidden.
	for name, b := range map[string]BuildYML{
		"runtime_depends": {RuntimeDepends: map[string][]string{"x": {"a:b"}}},
		"lockfile_deps":   {LockfileDeps: map[string][]string{"x": {"libc6"}}},
		"signing":         {Signing: []SigningKey{{KeyName: "k"}}},
	} {
		if err := ValidateToolingBuildYML(b); err == nil {
			t.Errorf("tooling build.yml with %s should be rejected", name)
		}
	}
}

func TestLoadImageTypeConf(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "conf.yml"), `debian_tooling:
  - e2fsprogs
  - fdisk
local_tooling:
  - e2fsprogs-repro:e2fsprogs
`)
	conf, err := LoadImageTypeConf(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(conf.DebianTooling) != 2 || conf.DebianTooling[0] != "e2fsprogs" {
		t.Errorf("debian_tooling = %v", conf.DebianTooling)
	}
	if len(conf.LocalTooling) != 1 || conf.LocalTooling[0] != "e2fsprogs-repro:e2fsprogs" {
		t.Errorf("local_tooling = %v", conf.LocalTooling)
	}
}

func TestLoadImageTypeConf_BadLocalTooling(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "conf.yml"), "local_tooling:\n  - just-a-source\n")
	if _, err := LoadImageTypeConf(dir); err == nil {
		t.Fatal("local_tooling without <source>:<binary> form should be rejected")
	}
}

func TestLoadImages(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "images.yml"), `images:
  - name: base-ext4
    rootfs: base
    type: plain-ext4
  - name: min-ext4
    rootfs: min
    type: plain-ext4
`)
	imgs, err := LoadImages(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 2 || imgs[0].Name != "base-ext4" || imgs[1].Rootfs != "min" {
		t.Errorf("images = %+v", imgs)
	}
}

func TestLoadImages_MissingIsNil(t *testing.T) {
	imgs, err := LoadImages(t.TempDir())
	if err != nil {
		t.Fatalf("missing images.yml should not error: %v", err)
	}
	if imgs != nil {
		t.Errorf("missing images.yml should yield nil, got %+v", imgs)
	}
}

func TestLoadImages_IncompleteEntryErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "images.yml"), "images:\n  - name: x\n    rootfs: base\n")
	if _, err := LoadImages(dir); err == nil {
		t.Fatal("images.yml entry missing type should be rejected")
	}
}

func TestLoadImages_Inputs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "images.yml"), `images:
  - name: initrd
    rootfs: base
    type: cpio
  - name: disk
    rootfs: base
    type: gpt
    inputs:
      - label: initramfs
        image: initrd
      - label: cmdline
        value: "console=ttyS0"
      - label: empty
        value: ""
`)
	imgs, err := LoadImages(dir)
	if err != nil {
		t.Fatal(err)
	}
	disk := imgs[1]
	if len(disk.Inputs) != 3 {
		t.Fatalf("disk inputs = %+v", disk.Inputs)
	}
	if disk.Inputs[0].Image != "initrd" || disk.Inputs[0].Value != nil {
		t.Errorf("image input = %+v", disk.Inputs[0])
	}
	if disk.Inputs[1].Value == nil || *disk.Inputs[1].Value != "console=ttyS0" {
		t.Errorf("value input = %+v", disk.Inputs[1])
	}
	if disk.Inputs[2].Value == nil || *disk.Inputs[2].Value != "" {
		t.Errorf("empty value input should be present, got %+v", disk.Inputs[2])
	}
}

func TestLoadImages_InputValidation(t *testing.T) {
	cases := map[string]string{
		"no label": `images:
  - {name: a, rootfs: base, type: t, inputs: [{value: v}]}
`,
		"both value and image": `images:
  - {name: a, rootfs: base, type: t, inputs: [{label: l, value: v, image: b}]}
`,
		"neither value nor image": `images:
  - {name: a, rootfs: base, type: t, inputs: [{label: l}]}
`,
		"duplicate label": `images:
  - {name: a, rootfs: base, type: t, inputs: [{label: l, value: x}, {label: l, value: y}]}
`,
	}
	for name, yml := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "images.yml"), yml)
			if _, err := LoadImages(dir); err == nil {
				t.Fatalf("%s should be rejected", name)
			}
		})
	}
}

func TestDiscoverImageTypes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "images.d", "plain-ext4", "conf.yml"), "debian_tooling: [e2fsprogs]\n")
	writeFile(t, filepath.Join(dir, "images.d", "gpt-bootable", "conf.yml"), "debian_tooling: [fdisk]\n")
	// tooling/ is reserved and never a type; a dir without conf.yml is skipped.
	writeFile(t, filepath.Join(dir, "images.d", "tooling", "e2fsprogs", "build.yml"), "pkg_type: debian\n")
	if err := os.MkdirAll(filepath.Join(dir, "images.d", "no-conf"), 0755); err != nil {
		t.Fatal(err)
	}
	types := DiscoverImageTypes(dir)
	want := []string{"gpt-bootable", "plain-ext4"}
	if len(types) != 2 || types[0] != want[0] || types[1] != want[1] {
		t.Errorf("DiscoverImageTypes = %v, want %v", types, want)
	}
}

func TestDiscoverImageTypes_NoImagesDir(t *testing.T) {
	if types := DiscoverImageTypes(t.TempDir()); types != nil {
		t.Errorf("no images.d should yield nil, got %v", types)
	}
}
