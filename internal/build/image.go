package build

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gardenlinux/glbx/internal/artifact"
	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// imageSeed is the per-type identity seed for disk images, folded in after the
// global seed so regenerating it invalidates exactly the image identities.
// Regenerate when a change makes image derivation incompatible with prior cache
// entries:
//
//	openssl rand -hex 16
const imageSeed = "e7c4a9d2f0b8153647ae9c2d1f6b3e85"

// imageOutputName is the fixed output name of an image's disk-image blob. The
// image type and format are already encoded in the node key; the script owns
// the on-disk format, and glbx stores whatever bytes it writes under this name.
const imageOutputName = "image"

// localToolingRef is one resolved local_tooling entry: the tooling build that
// produces it and the output name of the named binary's .deb.
type localToolingRef struct {
	build  *ToolingPkgBuild
	output string
}

// Image is the disk-image artifact and the new top of the graph. It runs an
// image type's assembly script inside a container populated from the type's
// tooling lock (plus any local-tooling overlays), handing the script the rootfs
// tarball as input and harvesting the disk image it writes as output.
type Image struct {
	Name     string // artifact label from images.yml
	Arch     string
	TypeDir  string // images.d/<type>/
	rootfs   *Rootfs
	conf     buildcfg.ImageTypeConf
	local    []localToolingRef
	baseDir  string
	store    *objstore.Store
	stubPath string
	identity objstore.Hash
}

// ImageConfig holds the parameters for constructing an Image artifact.
type ImageConfig struct {
	Name     string
	Arch     string
	TypeDir  string
	Rootfs   *Rootfs
	Conf     buildcfg.ImageTypeConf
	Local    []localToolingRef
	BaseDir  string // config-repo root (where tooling-deps.yml etc. are resolved)
	Store    *objstore.Store
	StubPath string
}

func newImage(cfg ImageConfig) *Image {
	return &Image{
		Name:     cfg.Name,
		Arch:     cfg.Arch,
		TypeDir:  cfg.TypeDir,
		rootfs:   cfg.Rootfs,
		conf:     cfg.Conf,
		local:    cfg.Local,
		baseDir:  cfg.BaseDir,
		store:    cfg.Store,
		stubPath: cfg.StubPath,
	}
}

func (im *Image) Key() string {
	return fmt.Sprintf("image:%s:%s", im.Name, im.Arch)
}

func (im *Image) ShortName() string { return im.Name }

// Depends are the one rootfs node and each local_tooling build. These are the
// only cross-universe edges: image → rootfs and image → tooling build.
func (im *Image) Depends() []artifact.Artifact {
	deps := []artifact.Artifact{im.rootfs}
	for _, l := range im.local {
		deps = append(deps, l.build)
	}
	return deps
}

func (im *Image) Includes() []artifact.Artifact { return nil }

func (im *Image) OutputRefs(store *objstore.Store) (objstore.Hash, []objstore.Hash, error) {
	return artifact.ResolveOutputRefs(im, store)
}

// Inputs references the rootfs tarball and each local_tooling build's named
// .deb, all resolved from their manifests before Build runs.
func (im *Image) Inputs() []artifact.Input {
	inputs := []artifact.Input{{Source: im.rootfs, Name: "rootfs.tar.gz"}}
	for _, l := range im.local {
		inputs = append(inputs, artifact.Input{Source: l.build, Name: l.output})
	}
	return inputs
}

// assemblyToolingPath is the type's generated assembly-tooling lock.
func (im *Image) assemblyToolingPath() string {
	return filepath.Join(im.TypeDir, "tooling-deps.yml")
}

// scriptPath is the type's assembly script.
func (im *Image) scriptPath() string {
	return filepath.Join(im.TypeDir, "build")
}

// confPath is the type's definition.
func (im *Image) confPath() string {
	return filepath.Join(im.TypeDir, "conf.yml")
}

// Identity folds the image seed, the name and arch, the rootfs identity, the
// assembly script bytes, the conf.yml bytes, every pinned assembly-tool file
// hash, and each local_tooling build's identity. Any change to the rootfs, the
// script, the tool set, or a local tool rebuilds the image.
func (im *Image) Identity() (objstore.Hash, error) {
	if !im.identity.IsZero() {
		return im.identity, nil
	}

	parts := []string{artifact.IDSeed(), imageSeed, im.Name, im.Arch}

	rootfsID, err := im.rootfs.Identity()
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("rootfs identity: %w", err)
	}
	parts = append(parts, rootfsID.String())

	scriptHash, err := hashFile(im.scriptPath())
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("hash assembly script: %w", err)
	}
	parts = append(parts, scriptHash)

	confHash, err := hashFile(im.confPath())
	if err != nil {
		return objstore.Hash{}, fmt.Errorf("hash conf.yml: %w", err)
	}
	parts = append(parts, confHash)

	if tools, err := buildcfg.LoadBuildDeps(im.assemblyToolingPath()); err == nil {
		for _, t := range tools {
			for _, f := range t.Files {
				parts = append(parts, f.Hash.String())
			}
		}
	}

	for _, l := range im.local {
		id, err := l.build.Identity()
		if err != nil {
			return objstore.Hash{}, fmt.Errorf("local tooling identity: %w", err)
		}
		parts = append(parts, id.String())
	}

	hash := objstore.ConcatHash(parts...)
	im.identity = hash
	return hash, nil
}

// Build assembles the disk image. It constructs the assembly container from the
// tooling lock (plus local-tooling overlays), stages the rootfs tarball and an
// output path, runs the type's script by its shebang with CAP_SYS_ADMIN and no
// loop device, and harvests the written image from the staging mount namespace.
func (im *Image) Build(ctx artifact.BuildContext) ([]artifact.Output, error) {
	store := ctx.Store
	l := log.From(ctx.Ctx, log.Rootfs)
	l.Info("assembling image %s", im.Name)

	rootfsHash, ok := ctx.Inputs["rootfs.tar.gz"]
	if !ok {
		return nil, fmt.Errorf("image %s: rootfs tarball not in resolved inputs", im.Name)
	}

	toolingPkgs, err := im.loadAssemblyTooling()
	if err != nil {
		return nil, err
	}
	l.Info("%d pinned assembly tool packages, %d local-tooling overlays", len(toolingPkgs), len(im.local))

	stubPath := im.stubPath
	if stubPath == "" {
		stubPath = container.StubPath()
	}
	stack, err := container.NewStack(container.StackConfig{Ctx: ctx.Ctx, StubPath: stubPath})
	if err != nil {
		return nil, err
	}
	defer stack.Close()
	mountNS := stack.MountNS

	work, err := mountNS.MkTempDir("/tmp", "glbx-image-")
	if err != nil {
		return nil, fmt.Errorf("mktempdir: %w", err)
	}
	if err := mountNS.Mount("tmpfs", work, "tmpfs", 0, "size=8g"); err != nil {
		return nil, fmt.Errorf("mount tmpfs: %w", err)
	}

	rootfsDir := work + "/rootfs"
	if err := mountNS.Mkdir(rootfsDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir container root: %w", err)
	}

	// Populate the assembly container root: the pinned tooling closure first,
	// then each local-tooling .deb on top so a local tool overrides the mirror.
	if err := extractDebsRaw(ctx.Ctx, mountNS, store, toolingPkgs, rootfsDir, log.Rootfs); err != nil {
		return nil, fmt.Errorf("extract assembly tooling: %w", err)
	}
	for _, l := range im.local {
		hash, ok := ctx.Inputs[l.output]
		if !ok {
			return nil, fmt.Errorf("image %s: local tooling %q not in resolved inputs", im.Name, l.output)
		}
		blobPath, err := store.Blobs.Path(hash)
		if err != nil {
			return nil, fmt.Errorf("local tooling blob %s: %w", l.output, err)
		}
		if err := container.Run(mountNS, &container.ExecRequest{
			Argv: []string{"dpkg-deb", "-x", blobPath, rootfsDir},
			Env:  []string{"DEBIAN_FRONTEND=noninteractive"},
			Cwd:  "/",
		}); err != nil {
			return nil, fmt.Errorf("extract local tooling %s: %w", l.output, err)
		}
	}

	for _, d := range []string{"proc", "sys", "dev", "tmp", "run"} {
		mountNS.Mkdir(rootfsDir+"/"+d, 0755)
	}

	// Stage the rootfs tarball read-only at a known path, and a writable staging
	// area for the output file, both inside what will become the container root.
	rootfsBlob, err := store.Blobs.Path(rootfsHash)
	if err != nil {
		return nil, fmt.Errorf("rootfs blob: %w", err)
	}
	stageDir := rootfsDir + "/glbx-image"
	if err := mountNS.Mkdir(stageDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir stage dir: %w", err)
	}
	rootfsIn := stageDir + "/rootfs.tar.gz"
	if err := mountNS.CreateFile(rootfsIn, 0644); err != nil {
		return nil, fmt.Errorf("create rootfs input mount point: %w", err)
	}
	if err := mountNS.Mount(rootfsBlob, rootfsIn, "", mountBindRO, ""); err != nil {
		return nil, fmt.Errorf("bind rootfs tarball: %w", err)
	}

	// Stage the assembly script into the container root and make it executable.
	scriptBytes, err := os.ReadFile(im.scriptPath())
	if err != nil {
		return nil, fmt.Errorf("read assembly script: %w", err)
	}
	scriptIn := stageDir + "/build"
	f, err := mountNS.Open(scriptIn, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return nil, fmt.Errorf("open script for write: %w", err)
	}
	if _, err := f.Write(scriptBytes); err != nil {
		f.Close()
		return nil, fmt.Errorf("write script: %w", err)
	}
	f.Close()

	cont, err := container.NewContainer(container.ContainerConfig{
		Ctx:      ctx.Ctx,
		Parent:   mountNS,
		StubPath: stubPath,
		Rootfs:   rootfsDir,
	})
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}

	// In-container paths: the staging dir is at /glbx-image after pivot_root.
	const inScript = "/glbx-image/build"
	const inRootfs = "/glbx-image/rootfs.tar.gz"
	const inOutput = "/glbx-image/image.out"

	l.Info("running assembly script")
	if err := execIn(ctx.Ctx, cont, []string{
		"HOME=/root",
		"LANG=C.UTF-8",
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
	}, nil, "/", inScript, inRootfs, inOutput); err != nil {
		cont.Close()
		return nil, fmt.Errorf("assembly script: %w", err)
	}
	cont.Close()

	// Harvest the written image from the staging mount namespace (never cat out
	// of the container): the file is visible in the parent MountNS at the staging
	// path under the tmpfs.
	outHostPath := stageDir + "/image.out"
	imgHash, err := harvestFileFromMountNS(mountNS, outHostPath, store)
	if err != nil {
		return nil, fmt.Errorf("harvest image output: %w", err)
	}

	return []artifact.Output{{Name: imageOutputName, Hash: imgHash}}, nil
}

// loadAssemblyTooling reads the type's pinned assembly-tooling lock into a
// metadata-bearing index and returns its packages for the target arch.
func (im *Image) loadAssemblyTooling() ([]*index.Package, error) {
	tools, err := buildcfg.LoadBuildDeps(im.assemblyToolingPath())
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", im.assemblyToolingPath(), err)
	}
	idx, err := pinnedToolingIndex(im.store, tools, im.Arch)
	if err != nil {
		return nil, err
	}
	return idx.All(), nil
}

// hashFile returns the hex SHA-256 of a file's content.
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha256HexBytes(data), nil
}
