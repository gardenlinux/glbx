package lockfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/debian/aptrepo"
	"github.com/gardenlinux/glbx/internal/debian/deb822"
	"github.com/gardenlinux/glbx/internal/debian/depends"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/resolver"
	"github.com/gardenlinux/glbx/internal/stream"
	"gopkg.in/yaml.v3"
)

// Config parameterizes generating a package's build-tooling lock.
type Config struct {
	Ctx       context.Context
	Store     *objstore.Store
	RepoURL   string
	Dist      string
	Arch      string
	OutputDir string
	PkgName   string
	Cookie    string

	// SnapshotBase is the hash-addressed snapshot file endpoint recorded as a
	// secondary retrieval URL for each .deb. Empty uses
	// aptrepo.DefaultSnapshotBase.
	SnapshotBase string
}

// Result summarizes a generated lock.
type Result struct {
	Arch     string
	Packages int
	Path     string
}

// Generate produces build-deps.yml for one package: it reads the package's
// declared build dependencies, resolves their closure against the binary index,
// fetches and hash-verifies every resolved .deb into the store, and writes the
// explicit per-architecture pinned-tooling file.
func Generate(cfg Config) (*Result, error) {
	cfg.applyDefaults()
	l := log.From(cfg.Ctx, log.Lockfile)

	pkgDir := filepath.Join(cfg.OutputDir, "pkgs", cfg.PkgName)
	controlPath := filepath.Join(pkgDir, "src", "debian", "control")
	buildDeps, err := extractBuildDeps(controlPath)
	if err != nil {
		return nil, fmt.Errorf("extract build-deps: %w", err)
	}

	buildYML, err := buildcfg.LoadBuildYML(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("load build.yml: %w", err)
	}

	idx, err := FetchBinaryIndex(cfg.Ctx, cfg.Store, cfg.RepoURL, cfg.Dist, cfg.Arch, cfg.Cookie)
	if err != nil {
		return nil, fmt.Errorf("fetch binary index: %w", err)
	}

	roots := buildResolverRoots(buildDeps, idx, cfg.Arch, buildYML.BuildProfiles)

	packages, err := resolveAndFetch(cfg.Ctx, cfg.Store, l, idx, roots, cfg.Arch, cfg.RepoURL)
	if err != nil {
		return nil, err
	}

	depsPath := filepath.Join(pkgDir, "build-deps.yml")
	if err := writeBuildDeps(depsPath, cfg.RepoURL, cfg.SnapshotBase, packages); err != nil {
		return nil, fmt.Errorf("write build-deps.yml: %w", err)
	}

	return &Result{Arch: cfg.Arch, Packages: len(packages), Path: depsPath}, nil
}

func (cfg *Config) applyDefaults() {
	applyRepoDefaults(&cfg.RepoURL, &cfg.Dist, &cfg.Arch, &cfg.Ctx)
}

func (cfg *RootfsConfig) applyDefaults() {
	applyRepoDefaults(&cfg.RepoURL, &cfg.Dist, &cfg.Arch, &cfg.Ctx)
}

// resolveAndFetch runs the resolver over roots, fetches and hash-verifies every
// resolved .deb into the store, and returns the resolved package set.
func resolveAndFetch(ctx context.Context, store *objstore.Store, l *log.Logger,
	idx *index.Index, roots []resolver.Requirement, arch, repoURL string,
) ([]*index.Package, error) {
	r := resolver.New(idx, arch)
	result, err := r.Resolve(roots)
	if err != nil {
		return nil, fmt.Errorf("resolve dependencies: %w", err)
	}

	l.Info("resolved %d packages, fetching .deb blobs", len(result.Packages))
	if err := FetchDebs(ctx, store, repoURL, result.Packages); err != nil {
		return nil, fmt.Errorf("fetch .deb blobs: %w", err)
	}
	return result.Packages, nil
}

// writeBuildDeps serializes the resolved package set as the explicit build-deps
// YAML: one tool per package, each with its exact version and a single file
// recording the architecture, content hash, and retrieval URLs of its .deb. The
// mirror URL is recorded first and the hash-addressed snapshot URL second.
func writeBuildDeps(path, repoURL, snapshotBase string, packages []*index.Package) error {
	doc := toolsDoc{Tools: make([]toolEntry, 0, len(packages))}
	for _, pkg := range packages {
		if pkg.SHA256 == "" || pkg.Filename == "" {
			return fmt.Errorf("package %s: missing hash or filename", pkg.Name)
		}
		if pkg.SHA1 == "" {
			return fmt.Errorf("package %s: missing SHA1 (not fetched)", pkg.Name)
		}
		doc.Tools = append(doc.Tools, toolEntry{
			Name:    pkg.Name,
			Version: pkg.Version,
			Files: []fileEntry{{
				Arch:   pkg.Architecture,
				SHA256: pkg.SHA256,
				URLs: []string{
					fmt.Sprintf("%s/%s", repoURL, pkg.Filename),
					aptrepo.SnapshotURL(snapshotBase, pkg.SHA1, filepath.Base(pkg.Filename)),
				},
			}},
		})
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal build-deps: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

type toolsDoc struct {
	Tools []toolEntry `yaml:"tools"`
}

type toolEntry struct {
	Name    string      `yaml:"name"`
	Version string      `yaml:"version"`
	Files   []fileEntry `yaml:"files"`
}

type fileEntry struct {
	Arch   string   `yaml:"arch"`
	SHA256 string   `yaml:"sha256"`
	URLs   []string `yaml:"urls"`
}

func extractBuildDeps(controlPath string) (depends.DependencyList, error) {
	f, err := os.Open(controlPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := deb822.NewReader(f)
	stanza, err := reader.Next()
	if err != nil {
		return nil, fmt.Errorf("read control stanza: %w", err)
	}

	var allDeps string
	appendField := func(key string) {
		if v, ok := stanza[key]; ok && v != "" {
			if allDeps != "" {
				allDeps += ", "
			}
			allDeps += v
		}
	}
	appendField("build-depends")
	appendField("build-depends-arch")
	appendField("build-depends-indep")

	if allDeps == "" {
		return nil, nil
	}
	return depends.Parse(allDeps)
}

// FetchBinaryIndex fetches, verifies against the signed Release, caches, and
// parses the binary Packages index for the given architecture.
func FetchBinaryIndex(ctx context.Context, store *objstore.Store, repoURL, dist, arch, cookie string) (*index.Index, error) {
	l := log.From(ctx, log.Lockfile)

	releasePayload, err := aptrepo.FetchInRelease(ctx, aptrepo.FetchConfig{
		Store:     store,
		RepoURL:   repoURL,
		Dist:      dist,
		Cookie:    cookie,
		NoVerify:  true,
		Component: log.Lockfile,
	})
	if err != nil {
		return nil, fmt.Errorf("fetch InRelease: %w", err)
	}

	releaseHashes, err := aptrepo.ParseReleaseHashes(releasePayload)
	if err != nil {
		return nil, fmt.Errorf("parse Release hashes: %w", err)
	}

	packagesPath := fmt.Sprintf("main/binary-%s/Packages.gz", arch)
	expectedHash, ok := releaseHashes[packagesPath]
	if !ok {
		return nil, fmt.Errorf("%s not found in Release file", packagesPath)
	}

	var packagesCompressed []byte
	packagesHash, hashErr := objstore.NewHash(expectedHash)
	if hashErr == nil && store.Blobs.Has(packagesHash) {
		l.Debug("Packages.gz cached (%s)", expectedHash[:12])
		rc, err := store.Blobs.Open(packagesHash)
		if err != nil {
			return nil, fmt.Errorf("read cached Packages.gz: %w", err)
		}
		packagesCompressed, err = io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read cached Packages.gz: %w", err)
		}
	} else {
		pkgURL := fmt.Sprintf("%s/dists/%s/%s", repoURL, dist, packagesPath)
		l.Info("downloading %s", pkgURL)
		resp, err := http.Get(pkgURL)
		if err != nil {
			return nil, fmt.Errorf("fetch Packages.gz: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("fetch Packages.gz: status %d", resp.StatusCode)
		}
		packagesCompressed, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("read Packages.gz: %w", err)
		}

		if sha256Hex(packagesCompressed) != expectedHash {
			return nil, fmt.Errorf("Packages.gz hash mismatch")
		}
		if _, err := store.Blobs.Store(bytes.NewReader(packagesCompressed)); err != nil {
			l.Warn("failed to cache Packages.gz: %v", err)
		}
	}

	decompressed, err := stream.GzipDecompress(bytes.NewReader(packagesCompressed))
	if err != nil {
		return nil, fmt.Errorf("decompress Packages.gz: %w", err)
	}
	defer decompressed.Close()

	return index.Load(decompressed)
}

// buildResolverRoots assembles the resolver roots for a package's build
// dependencies: the index essentials, the implicit build infrastructure, and
// each arch- and profile-active build dependency from the control file.
func buildResolverRoots(buildDeps depends.DependencyList, idx *index.Index, arch string, activeProfiles []string) []resolver.Requirement {
	var roots []resolver.Requirement

	for _, pkg := range idx.EssentialPackages() {
		roots = append(roots, resolver.Requirement{Name: pkg.Name})
	}
	for _, name := range []string{"build-essential", "fakeroot", "debconf"} {
		roots = append(roots, resolver.Requirement{Name: name})
	}

	for _, alt := range buildDeps {
		var matched *depends.Dependency
		for i := range alt {
			if alt[i].MatchesArch(arch) && !alt[i].ExcludedByProfiles(activeProfiles) {
				matched = &alt[i]
				break
			}
		}
		if matched == nil {
			continue
		}
		req := resolver.Requirement{Name: matched.Name, VirtualEligible: true}
		if matched.Version != nil {
			req.VersionOp = matched.Version.Op
			req.Version = matched.Version.Version
		}
		roots = append(roots, req)
	}

	return roots
}

// RootfsConfig parameterizes generating the image configuration-tooling lock.
type RootfsConfig struct {
	Ctx       context.Context
	Store     *objstore.Store
	RepoURL   string
	Dist      string
	Arch      string
	OutputDir string
	Cookie    string

	// SnapshotBase is the hash-addressed snapshot file endpoint recorded as a
	// secondary retrieval URL for each .deb. Empty uses
	// aptrepo.DefaultSnapshotBase.
	SnapshotBase string
}

// GenerateRootfs produces rootfs-deps.yml: the Debian packaging infrastructure
// (dpkg, the essential set, perl-base, mawk) present only while image
// configuration scripts run and excluded from the final image. It uses the
// same explicit-YAML form as a package's build-deps.yml.
func GenerateRootfs(cfg RootfsConfig) (*Result, error) {
	cfg.applyDefaults()
	l := log.From(cfg.Ctx, log.Lockfile)

	idx, err := FetchBinaryIndex(cfg.Ctx, cfg.Store, cfg.RepoURL, cfg.Dist, cfg.Arch, cfg.Cookie)
	if err != nil {
		return nil, fmt.Errorf("fetch binary index: %w", err)
	}

	roots := buildRootfsRoots(idx)

	packages, err := resolveAndFetch(cfg.Ctx, cfg.Store, l, idx, roots, cfg.Arch, cfg.RepoURL)
	if err != nil {
		return nil, err
	}

	depsPath := filepath.Join(cfg.OutputDir, "rootfs-deps.yml")
	if err := writeBuildDeps(depsPath, cfg.RepoURL, cfg.SnapshotBase, packages); err != nil {
		return nil, fmt.Errorf("write rootfs-deps.yml: %w", err)
	}

	return &Result{Arch: cfg.Arch, Packages: len(packages), Path: depsPath}, nil
}

// buildRootfsRoots assembles the resolver roots for the image configuration
// tooling: every Essential package plus the postinst-script infrastructure
// (perl-base for perl scripts, mawk as the default awk). apt is excluded —
// configuration is dpkg-only.
func buildRootfsRoots(idx *index.Index) []resolver.Requirement {
	var roots []resolver.Requirement
	for _, pkg := range idx.EssentialPackages() {
		roots = append(roots, resolver.Requirement{Name: pkg.Name})
	}
	for _, name := range []string{"perl-base", "mawk"} {
		roots = append(roots, resolver.Requirement{Name: name})
	}
	return roots
}

func applyRepoDefaults(repoURL, dist, arch *string, ctx *context.Context) {
	if *repoURL == "" {
		*repoURL = "https://deb.debian.org/debian"
	}
	if *dist == "" {
		*dist = "testing"
	}
	if *arch == "" {
		*arch = buildcfg.HostArch()
	}
	if *ctx == nil {
		*ctx = context.Background()
	}
}

// FetchDebs downloads and hash-verifies every package's .deb into the store,
// 16 at a time, skipping those already present.
func FetchDebs(ctx context.Context, store *objstore.Store, repoURL string, packages []*index.Package) error {
	l := log.From(ctx, log.Fetch)
	var wg sync.WaitGroup
	errCh := make(chan error, len(packages))
	sem := make(chan struct{}, 16)

	var fetched, cached atomic.Int32
	total := len(packages)

	for _, pkg := range packages {
		wg.Add(1)
		go func(p *index.Package) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if p.SHA256 == "" {
				return
			}
			h, err := objstore.NewHash(p.SHA256)
			if err != nil {
				errCh <- fmt.Errorf("%s: invalid hash: %w", p.Name, err)
				return
			}
			if store.Blobs.Has(h) {
				cached.Add(1)
				r, err := store.Blobs.Open(h)
				if err != nil {
					errCh <- fmt.Errorf("open cached %s: %w", p.Name, err)
					return
				}
				sum, err := stream.SHA1Reader(r)
				r.Close()
				if err != nil {
					errCh <- fmt.Errorf("hash cached %s: %w", p.Name, err)
					return
				}
				p.SHA1 = sum
				return
			}
			if p.Filename == "" {
				errCh <- fmt.Errorf("%s: no Filename field", p.Name)
				return
			}

			url := fmt.Sprintf("%s/%s", repoURL, p.Filename)
			resp, err := http.Get(url)
			if err != nil {
				errCh <- fmt.Errorf("fetch %s: %w", p.Name, err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errCh <- fmt.Errorf("fetch %s: HTTP %d", p.Name, resp.StatusCode)
				return
			}

			data, err := io.ReadAll(resp.Body)
			if err != nil {
				errCh <- fmt.Errorf("read %s: %w", p.Name, err)
				return
			}
			if sha256Hex(data) != p.SHA256 {
				errCh <- fmt.Errorf("hash mismatch for %s", p.Name)
				return
			}
			if _, err := store.Blobs.Store(bytes.NewReader(data)); err != nil {
				errCh <- fmt.Errorf("store %s: %w", p.Name, err)
				return
			}
			p.SHA1 = stream.SHA1Bytes(data)

			n := fetched.Add(1)
			if n%10 == 0 || int(n)+int(cached.Load()) == total {
				l.Info("%d/%d fetched, %d cached", n, total, cached.Load())
			}
		}(pkg)
	}

	wg.Wait()
	close(errCh)

	l.Info("fetch complete: %d fetched, %d cached", fetched.Load(), cached.Load())

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d fetch errors (first: %w)", len(errs), errs[0])
	}
	return nil
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
