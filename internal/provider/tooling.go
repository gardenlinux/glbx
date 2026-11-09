package provider

import (
	"path/filepath"

	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/importer"
	"github.com/gardenlinux/glbx/internal/lockfile"
	"github.com/gardenlinux/glbx/internal/log"
)

// toolingSubDir is the working-tree subtree the tooling universe lives in. It
// keeps an assembly-time tool and a same-named shipped package as separate
// artifacts: the import, lock, and build of a tooling package all route through
// this path, never pkgs/.
const toolingSubDir = "images.d/tooling"

func init() {
	RegisterImporter(toolingDebianImporter{})
	RegisterLockGenerator(toolingPkgLock{})
	RegisterLockGenerator(imageTypeLock{})
}

// toolingDebianImporter imports a Debian source package into the tooling
// universe (images.d/tooling/<name>/) with the reduced tooling schema. It is
// the same Debian importer as debian:, differing only in the destination
// subtree, so a pkgs/ import and a tooling import of the same upstream name are
// two different, coexisting packages.
type toolingDebianImporter struct{}

func (toolingDebianImporter) Namespace() string { return "tooling-debian" }

func (toolingDebianImporter) ParamsHelp() string {
	return debianImporter{}.ParamsHelp()
}

func (toolingDebianImporter) Import(op OpContext, name string, opts ImportOptions) (*ImportResult, error) {
	cfg, err := debianRepoConfig(op.Params)
	if err != nil {
		return nil, err
	}

	result, err := importer.Import(importer.ImportConfig{
		Ctx:          op.Ctx,
		Store:        op.Store,
		RepoURL:      cfg.Repo,
		SnapshotBase: cfg.Snapshot,
		Dist:         cfg.Dist,
		UpdateTag:    opts.UpdateTag,
		Keyring:      cfg.Keyring,
		OutputDir:    op.OutputDir,
		NoVerify:     cfg.NoVerify,
		GitHistory:   opts.GitHistory,
		Cookie:       cfg.Cookie,
		SubDir:       toolingSubDir,
	}, name)
	if err != nil {
		return nil, err
	}

	out := &ImportResult{
		Name:           result.Name,
		Version:        result.Version,
		Format:         result.Format,
		Committed:      result.Committed,
		CommitHash:     result.CommitHash,
		FirstImport:    result.FirstImport,
		AlreadyPresent: result.AlreadyPresent,
	}
	for _, s := range result.Sources {
		out.Sources = append(out.Sources, ImportedSource{Name: s.Name, Hash: s.Hash.String()})
	}
	return out, nil
}

// toolingPkgLock generates the build-tooling lock for a tooling package:
// images.d/tooling/<name>/build-deps.yml. It is the same resolve debian: does,
// rooted at the tooling tree, and validates the reduced tooling schema after
// loading build.yml.
type toolingPkgLock struct{}

func (toolingPkgLock) Namespace() string  { return "tooling-pkg" }
func (toolingPkgLock) ParamsHelp() string { return debianLockParamsHelp }

func (toolingPkgLock) Generate(op OpContext, arg string) error {
	cfg, err := debianRepoConfig(op.Params)
	if err != nil {
		return err
	}

	pkgDir := filepath.Join(op.OutputDir, toolingSubDir, arg)
	buildYML, err := buildcfg.LoadBuildYML(pkgDir)
	if err != nil {
		return err
	}
	if err := buildcfg.ValidateToolingBuildYML(buildYML); err != nil {
		return err
	}

	result, err := lockfile.Generate(lockfile.Config{
		Ctx:          op.Ctx,
		Store:        op.Store,
		RepoURL:      cfg.Repo,
		SnapshotBase: cfg.Snapshot,
		Dist:         cfg.Dist,
		Arch:         cfg.Arch,
		OutputDir:    op.OutputDir,
		PkgName:      arg,
		SubDir:       toolingSubDir,
		Cookie:       cfg.Cookie,
	})
	if err != nil {
		return err
	}
	log.From(op.Ctx, log.Lockfile).Info("generated %s (%s): %d packages", result.Path, result.Arch, result.Packages)
	return nil
}

// imageTypeLock generates an image type's assembly-tooling lock:
// images.d/<name>/tooling-deps.yml. It reads the type's conf.yml, resolves the
// essential floor plus every debian_tooling name's runtime closure, and pins
// them. local_tooling is ignored here — those are graph edges, not mirror pins.
type imageTypeLock struct{}

func (imageTypeLock) Namespace() string  { return "image-type" }
func (imageTypeLock) ParamsHelp() string { return debianLockParamsHelp }

func (imageTypeLock) Generate(op OpContext, arg string) error {
	cfg, err := debianRepoConfig(op.Params)
	if err != nil {
		return err
	}

	typeDir := filepath.Join(op.OutputDir, "images.d", arg)
	conf, err := buildcfg.LoadImageTypeConf(typeDir)
	if err != nil {
		return err
	}

	result, err := lockfile.GenerateImageType(lockfile.ImageTypeConfig{
		Ctx:          op.Ctx,
		Store:        op.Store,
		RepoURL:      cfg.Repo,
		SnapshotBase: cfg.Snapshot,
		Dist:         cfg.Dist,
		Arch:         cfg.Arch,
		OutputDir:    op.OutputDir,
		Cookie:       cfg.Cookie,
		TypeName:     arg,
		ExtraRoots:   conf.DebianTooling,
	})
	if err != nil {
		return err
	}
	log.From(op.Ctx, log.Lockfile).Info("generated %s (%s): %d packages", result.Path, result.Arch, result.Packages)
	return nil
}
