package provider

import (
	"fmt"

	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/debian/aptrepo"
	"github.com/gardenlinux/glbx/internal/importer"
	"github.com/gardenlinux/glbx/internal/lockfile"
	"github.com/gardenlinux/glbx/internal/log"
)

func init() {
	RegisterLockGenerator(debianLock{})
	RegisterImporter(debianImporter{})
}

// debianRepoCfg is the validated, defaulted set of Debian-family repository
// parameters shared by every Debian-family provider.
type debianRepoCfg struct {
	Repo     string
	Dist     string
	Snapshot string
	Arch     string
	Cookie   string
	Keyring  string
	NoVerify bool

	// Gen* override Repo/Dist/Arch for a provider's control-generation container
	// (debian-kernel). They default to the resolved Repo/Dist/Arch.
	GenRepo string
	GenDist string
	GenArch string
}

// debianParamKeys is the set of parameter keys the Debian-family providers
// accept. Keys outside this set are rejected so a typo surfaces as an error
// rather than a silently ignored option.
var debianParamKeys = map[string]bool{
	"repo": true, "dist": true, "snapshot": true, "arch": true,
	"cookie": true, "keyring": true, "no-verify": true,
	"gen-repo": true, "gen-dist": true, "gen-arch": true,
}

// debianRepoConfig validates the Debian-family parameters, rejecting any
// unknown key, and applies defaults. It is the single place Debian param
// validation lives, so every Debian-family provider shares one contract.
func debianRepoConfig(p Params) (debianRepoCfg, error) {
	for k := range p {
		if !debianParamKeys[k] {
			return debianRepoCfg{}, fmt.Errorf("unknown param %q for a debian-family provider", k)
		}
	}

	cfg := debianRepoCfg{
		Repo:     p["repo"],
		Dist:     p["dist"],
		Snapshot: p["snapshot"],
		Arch:     p["arch"],
		Cookie:   p["cookie"],
		Keyring:  p["keyring"],
		GenRepo:  p["gen-repo"],
		GenDist:  p["gen-dist"],
		GenArch:  p["gen-arch"],
	}
	if v, ok := p["no-verify"]; ok {
		switch v {
		case "", "true", "1", "yes":
			cfg.NoVerify = true
		case "false", "0", "no":
			cfg.NoVerify = false
		default:
			return debianRepoCfg{}, fmt.Errorf("param no-verify=%q is not a boolean", v)
		}
	}

	if cfg.Repo == "" {
		cfg.Repo = "https://deb.debian.org/debian"
	}
	if cfg.Dist == "" {
		cfg.Dist = "testing"
	}
	if cfg.Snapshot == "" {
		cfg.Snapshot = aptrepo.DefaultSnapshotBase
	}
	if cfg.Arch == "" {
		cfg.Arch = buildcfg.HostArch()
	}
	if cfg.Keyring == "" {
		cfg.Keyring = "/usr/share/keyrings/debian-archive-keyring.gpg"
	}
	if cfg.GenRepo == "" {
		cfg.GenRepo = cfg.Repo
	}
	if cfg.GenDist == "" {
		cfg.GenDist = cfg.Dist
	}
	if cfg.GenArch == "" {
		cfg.GenArch = cfg.Arch
	}
	return cfg, nil
}

const debianLockParamsHelp = `repo      APT repository URL; default https://deb.debian.org/debian
dist      APT distribution; default testing
snapshot  snapshot.debian.org file endpoint (SHA1-addressed); default <builtin>
arch      target architecture; default host arch
cookie    reuse a cached InRelease within a session`

// debianLock is the plain Debian lock generator: it reads the package's
// src/debian/control, resolves the build-dependency closure, fetches every
// .deb, and writes pkgs/<name>/build-deps.yml. It runs no container.
type debianLock struct{}

func (debianLock) Namespace() string  { return "debian" }
func (debianLock) ParamsHelp() string { return debianLockParamsHelp }

func (debianLock) Generate(op OpContext, arg string) error {
	cfg, err := debianRepoConfig(op.Params)
	if err != nil {
		return err
	}
	return runDebianPkgLock(op, arg, cfg)
}

// runDebianPkgLock resolves and writes a package's build-deps.yml from its
// current src/debian/control. It is the shared Debian lock path the plain
// debian provider and the debian-kernel provider (after regenerating control)
// both end in.
func runDebianPkgLock(op OpContext, name string, cfg debianRepoCfg) error {
	result, err := lockfile.Generate(lockfile.Config{
		Ctx:          op.Ctx,
		Store:        op.Store,
		RepoURL:      cfg.Repo,
		SnapshotBase: cfg.Snapshot,
		Dist:         cfg.Dist,
		Arch:         cfg.Arch,
		OutputDir:    op.OutputDir,
		PkgName:      name,
		Cookie:       cfg.Cookie,
	})
	if err != nil {
		return err
	}
	log.From(op.Ctx, log.Lockfile).Info("generated %s (%s): %d packages", result.Path, result.Arch, result.Packages)
	return nil
}

// debianImporter is the plain Debian importer; it wraps the import pipeline.
type debianImporter struct{}

func (debianImporter) Namespace() string { return "debian" }

func (debianImporter) ParamsHelp() string {
	return `repo       APT repository URL; default https://deb.debian.org/debian
dist       APT distribution; default testing
snapshot   snapshot.debian.org file endpoint (SHA1-addressed); default <builtin>
keyring    GPG keyring path; default /usr/share/keyrings/debian-archive-keyring.gpg
cookie     reuse a cached InRelease within a session
no-verify  skip GPG signature verification`
}

func (debianImporter) Import(op OpContext, name string, opts ImportOptions) (*ImportResult, error) {
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
