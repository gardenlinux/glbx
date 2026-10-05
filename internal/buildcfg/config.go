// Package buildcfg holds the on-disk YAML schemas and their typed loaders:
// the hand-authored per-package build declaration (build.yml), the generated
// source pins (sources.yml), the generated tooling pins (build-deps.yml), and
// the image package list (rootfs.yml). It is a neutral configuration package
// so that other packages can consume these shapes without importing each other.
package buildcfg

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gardenlinux/glbx/internal/objstore"
	"gopkg.in/yaml.v3"
)

// BuildYML is the hand-authored per-package build declaration, build.yml.
type BuildYML struct {
	BuildProfiles []string `yaml:"build_profiles"`
	BuildOptions  []string `yaml:"build_options"`
	ExtraBuildEnv []string `yaml:"extra_build_env"`
	// BuildDepends names outputs of other source builds needed in this
	// package's build chroot at compile time, each a "<source>:<binary>" pair.
	BuildDepends []string `yaml:"build_depends"`
	// RuntimeDepends maps an output binary name to the other locally built
	// binaries ("<source>:<binary>") that belong in its runtime closure.
	RuntimeDepends map[string][]string `yaml:"runtime_depends"`
	// LockfileDeps maps an output binary name to external package names
	// tolerated in that binary's validation: dependencies satisfied from the
	// pinned tooling rather than locally built. Per-binary and not inherited.
	LockfileDeps map[string][]string `yaml:"lockfile_deps"`
}

// LoadBuildYML reads build.yml from a package directory, returning the zero
// value when the file is absent.
func LoadBuildYML(pkgDir string) (BuildYML, error) {
	var out BuildYML
	data, err := os.ReadFile(filepath.Join(pkgDir, "build.yml"))
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("buildcfg: parse build.yml: %w", err)
	}
	return out, nil
}

// SourceArchive is one pinned upstream archive from sources.yml: its file name,
// its content hash (the identity), and the ordered retrieval locations used
// only on a cold cache.
type SourceArchive struct {
	File string
	Hash objstore.Hash
	URLs []string
}

type sourcesYML struct {
	Sources []struct {
		File   string   `yaml:"file"`
		SHA256 string   `yaml:"sha256"`
		URLs   []string `yaml:"urls"`
	} `yaml:"sources"`
}

// LoadSourcesYML reads sources.yml from a package directory. A missing file is
// reported with os.IsNotExist, which callers read as "native package, no
// external archives".
func LoadSourcesYML(pkgDir string) ([]SourceArchive, error) {
	data, err := os.ReadFile(filepath.Join(pkgDir, "sources.yml"))
	if err != nil {
		return nil, err
	}
	var raw sourcesYML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("buildcfg: parse sources.yml: %w", err)
	}
	out := make([]SourceArchive, 0, len(raw.Sources))
	for _, e := range raw.Sources {
		h, err := objstore.NewHash(e.SHA256)
		if err != nil {
			return nil, fmt.Errorf("buildcfg: sources.yml %q: %w", e.File, err)
		}
		out = append(out, SourceArchive{File: e.File, Hash: h, URLs: e.URLs})
	}
	return out, nil
}

// PinnedFile is one architecture variant of a pinned .deb: its target
// architecture ("all" for arch-independent), content hash, and retrieval URLs.
type PinnedFile struct {
	Arch string
	Hash objstore.Hash
	URLs []string
}

// PinnedTool is one external tool pinned in build-deps.yml: a package name, its
// exact Debian version, and one file per architecture variant.
type PinnedTool struct {
	Name    string
	Version string
	Files   []PinnedFile
}

type buildDepsYML struct {
	Tools []struct {
		Name    string `yaml:"name"`
		Version string `yaml:"version"`
		Files   []struct {
			Arch   string   `yaml:"arch"`
			SHA256 string   `yaml:"sha256"`
			URLs   []string `yaml:"urls"`
		} `yaml:"files"`
	} `yaml:"tools"`
}

// LoadBuildDeps reads build-deps.yml from path and returns the pinned tools.
func LoadBuildDeps(path string) ([]PinnedTool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw buildDepsYML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("buildcfg: parse build-deps.yml: %w", err)
	}
	out := make([]PinnedTool, 0, len(raw.Tools))
	for _, t := range raw.Tools {
		tool := PinnedTool{Name: t.Name, Version: t.Version}
		for _, f := range t.Files {
			h, err := objstore.NewHash(f.SHA256)
			if err != nil {
				return nil, fmt.Errorf("buildcfg: build-deps.yml %q: %w", t.Name, err)
			}
			tool.Files = append(tool.Files, PinnedFile{Arch: f.Arch, Hash: h, URLs: f.URLs})
		}
		out = append(out, tool)
	}
	return out, nil
}

// FilesForArch returns the pinned files to install for a target architecture:
// each tool's arch-matching file, or its single "all" file.
func FilesForArch(tools []PinnedTool, arch string) []PinnedFile {
	var out []PinnedFile
	for _, t := range tools {
		for _, f := range t.Files {
			if f.Arch == arch || f.Arch == "all" {
				out = append(out, f)
			}
		}
	}
	return out
}

type rootfsYML struct {
	Packages []string `yaml:"packages"`
}

// LoadRootfsYML reads the image package list from rootfs.yml in dir. A missing
// or malformed file yields nil.
func LoadRootfsYML(dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "rootfs.yml"))
	if err != nil {
		return nil
	}
	var raw rootfsYML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return raw.Packages
}
