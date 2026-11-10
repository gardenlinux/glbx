// Package buildcfg holds the on-disk YAML schemas and their typed loaders:
// the hand-authored per-package build declaration (build.yml), the generated
// source pins (sources.yml), the generated tooling pins (build-deps.yml), and
// the per-variant image package lists (rootfs-<label>.yml). It is a neutral configuration package
// so that other packages can consume these shapes without importing each other.
package buildcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gardenlinux/glbx/internal/objstore"
	"gopkg.in/yaml.v3"
)

// BuildYML is the hand-authored per-package build declaration, build.yml.
type BuildYML struct {
	// PkgType selects the lockfile provider the pkg: generator dispatches to
	// (e.g. "debian", "debian-kernel"). It is required to generate a lock
	// through the pkg: forwarder, which errors when it is empty; there is no
	// default. It has no effect on the build itself.
	PkgType string `yaml:"pkg_type"`

	// GenControlPkgs lists packages placed into the control-generation container
	// on top of the essential set. It is consumed only by lockfile providers
	// that regenerate a control file before locking (pkg_type: debian-kernel).
	GenControlPkgs []string `yaml:"gen_control_pkgs"`

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

	// Signing requests out-of-container signing for this package. When non-empty,
	// the build runs a host-side signing service per entry and makes it reachable
	// inside the container over a Unix socket, so the package can sign artifacts
	// without the private key ever entering the container. Each entry names a key
	// (its socket mount point), the host-side private key and public cert, and
	// where the cert is made visible to the build.
	Signing []SigningKey `yaml:"signing"`
}

// SigningKey is one signing identity exposed to a build. The private key and the
// certificate both stay on the host (the signing service reads them) and are
// served to the build over the signing socket; neither is a file in the
// container.
type SigningKey struct {
	// KeyName identifies the key and dictates its socket mount point inside the
	// container: /glbx/signing-service/<KeyName>. Keep it short.
	KeyName string `yaml:"key_name"`
	// PrivateKeyFile is the host-side PEM private key, resolved relative to the
	// conf dir. It is never mounted or copied into the container.
	PrivateKeyFile string `yaml:"private_key_file"`
	// CertFile is the host-side public X.509 cert matching the key, resolved
	// relative to the conf dir. Its content is folded into the build identity and
	// it is served to the build over the signing socket.
	CertFile string `yaml:"cert_file"`
}

// ValidateToolingBuildYML rejects fields that imply a runtime or validation
// phase a tooling package does not have. A tooling package is a Debian source
// build and nothing more: it ships no runtime closure and is never subjected to
// the binary-package locality or install checks, so declaring runtime_depends
// or lockfile_deps — or requesting out-of-container signing, which only a
// shipped package's build uses — is a configuration error.
func ValidateToolingBuildYML(b BuildYML) error {
	if len(b.RuntimeDepends) > 0 {
		return fmt.Errorf("buildcfg: tooling build.yml must not declare runtime_depends")
	}
	if len(b.LockfileDeps) > 0 {
		return fmt.Errorf("buildcfg: tooling build.yml must not declare lockfile_deps")
	}
	if len(b.Signing) > 0 {
		return fmt.Errorf("buildcfg: tooling build.yml must not declare signing")
	}
	return nil
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

// DiscoverRootfsLabels lists the image variants declared in dir. Each
// rootfs-<label>.yml file names one variant; the returned labels are sorted.
// rootfs-deps.yml is the shared image-configuration tooling lock, not a variant,
// and is excluded.
func DiscoverRootfsLabels(dir string) []string {
	matches, err := filepath.Glob(filepath.Join(dir, "rootfs-*.yml"))
	if err != nil {
		return nil
	}
	var labels []string
	for _, m := range matches {
		label := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "rootfs-"), ".yml")
		if label == "" || label == "deps" {
			continue
		}
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return labels
}

// LoadRootfsLabel reads the package list of the rootfs-<label>.yml variant in
// dir. A missing or malformed file yields nil.
func LoadRootfsLabel(dir, label string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "rootfs-"+label+".yml"))
	if err != nil {
		return nil
	}
	var raw rootfsYML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	return raw.Packages
}

// ImageTypeConf is an image type's definition (images.d/<type>/conf.yml): the
// two lists of assembly tools its container must contain. The assembly script
// itself (images.d/<type>/build) is run by its shebang and is not named here.
type ImageTypeConf struct {
	// DebianTooling names mirror binary packages resolved and pinned, with their
	// runtime closure, into the type's tooling-deps.yml on top of the essential
	// floor.
	DebianTooling []string `yaml:"debian_tooling"`
	// LocalTooling names locally built tooling in "<source>:<binary>" form,
	// pointing into images.d/tooling/<source>. These are graph edges to the
	// tooling build node, overlaid on the mirrored tools, not mirror pins.
	LocalTooling []string `yaml:"local_tooling"`
}

type imageTypeConfYML struct {
	DebianTooling []string `yaml:"debian_tooling"`
	LocalTooling  []string `yaml:"local_tooling"`
}

// LoadImageTypeConf reads conf.yml from an image type directory and validates
// that each local_tooling entry is in "<source>:<binary>" form.
func LoadImageTypeConf(typeDir string) (ImageTypeConf, error) {
	var out ImageTypeConf
	data, err := os.ReadFile(filepath.Join(typeDir, "conf.yml"))
	if err != nil {
		return out, err
	}
	var raw imageTypeConfYML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return out, fmt.Errorf("buildcfg: parse conf.yml: %w", err)
	}
	for _, spec := range raw.LocalTooling {
		src, bin, ok := strings.Cut(spec, ":")
		if !ok || src == "" || bin == "" {
			return out, fmt.Errorf("buildcfg: conf.yml local_tooling %q must be in <source>:<binary> form", spec)
		}
	}
	out.DebianTooling = raw.DebianTooling
	out.LocalTooling = raw.LocalTooling
	return out, nil
}

// ImageInput is one entry of an image's inputs list: a named piece of material
// staged into the assembly container at inputs/<Label>. Exactly one of Value or
// Image is set. A Value is a literal string written as a regular file; an Image
// names another image whose output is bind-mounted read-only and which becomes a
// build dependency. Value is a pointer so that an explicit empty string
// (value: "") is distinguishable from an unset field.
type ImageInput struct {
	Label string  `yaml:"label"`
	Value *string `yaml:"value"`
	Image string  `yaml:"image"`
}

// ImageSpec is one entry of the top-level images.yml: a concrete image pairing
// a rootfs variant with an image type under a developer-chosen artifact label,
// plus any inputs its assembly script consumes.
type ImageSpec struct {
	Name   string       `yaml:"name"`   // artifact label, surfaced as image:<name>:<arch>
	Rootfs string       `yaml:"rootfs"` // a rootfs-<label>.yml label
	Type   string       `yaml:"type"`   // an images.d/<type>/ directory
	Inputs []ImageInput `yaml:"inputs"`
}

type imagesYML struct {
	Images []ImageSpec `yaml:"images"`
}

// LoadImages reads the top-level images.yml spec list from confDir. A missing
// file is not an error — it means the config repo has no image layer — and
// yields nil.
func LoadImages(confDir string) ([]ImageSpec, error) {
	data, err := os.ReadFile(filepath.Join(confDir, "images.yml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var raw imagesYML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("buildcfg: parse images.yml: %w", err)
	}
	for _, img := range raw.Images {
		if img.Name == "" || img.Rootfs == "" || img.Type == "" {
			return nil, fmt.Errorf("buildcfg: images.yml entry must set name, rootfs, and type (got %+v)", img)
		}
		seen := make(map[string]bool, len(img.Inputs))
		for _, in := range img.Inputs {
			if in.Label == "" {
				return nil, fmt.Errorf("buildcfg: images.yml image %q has an input with no label", img.Name)
			}
			hasValue := in.Value != nil
			hasImage := in.Image != ""
			if hasValue == hasImage {
				return nil, fmt.Errorf("buildcfg: images.yml image %q input %q must set exactly one of value or image", img.Name, in.Label)
			}
			if seen[in.Label] {
				return nil, fmt.Errorf("buildcfg: images.yml image %q has duplicate input label %q", img.Name, in.Label)
			}
			seen[in.Label] = true
		}
	}
	return raw.Images, nil
}

// DiscoverImageTypes lists the image types declared under confDir/images.d/:
// each subdirectory carrying a conf.yml, excluding the reserved tooling/ tree.
// The returned names are sorted. A missing images.d/ yields nil.
func DiscoverImageTypes(confDir string) []string {
	entries, err := os.ReadDir(filepath.Join(confDir, "images.d"))
	if err != nil {
		return nil
	}
	var types []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "tooling" {
			continue
		}
		if _, err := os.Stat(filepath.Join(confDir, "images.d", e.Name(), "conf.yml")); err != nil {
			continue
		}
		types = append(types, e.Name())
	}
	sort.Strings(types)
	return types
}
