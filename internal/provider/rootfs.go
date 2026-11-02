package provider

import (
	"github.com/gardenlinux/glbx/internal/lockfile"
	"github.com/gardenlinux/glbx/internal/log"
)

func init() {
	RegisterLockGenerator(rootfsLock{})
}

// rootfsLock is the zero-argument image configuration-tooling lock generator.
// It resolves the Debian packaging infrastructure present only while image
// configuration scripts run (the Essential set plus perl-base and mawk) and
// writes the top-level rootfs-deps.yml. It runs no container and ignores its
// argument, which is always empty.
type rootfsLock struct{}

func (rootfsLock) Namespace() string { return "rootfs" }

func (rootfsLock) ParamsHelp() string { return debianLockParamsHelp }

func (rootfsLock) Generate(op OpContext, _ string) error {
	cfg, err := debianRepoConfig(op.Params)
	if err != nil {
		return err
	}
	result, err := lockfile.GenerateRootfs(lockfile.RootfsConfig{
		Ctx:          op.Ctx,
		Store:        op.Store,
		RepoURL:      cfg.Repo,
		SnapshotBase: cfg.Snapshot,
		Dist:         cfg.Dist,
		Arch:         cfg.Arch,
		OutputDir:    op.OutputDir,
		Cookie:       cfg.Cookie,
	})
	if err != nil {
		return err
	}
	log.From(op.Ctx, log.Lockfile).Info("generated %s (%s): %d packages", result.Path, result.Arch, result.Packages)
	return nil
}
