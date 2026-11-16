package main

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// findConfDir walks up from the working directory to the working-tree root,
// identified by a pkgs/ directory alongside at least one rootfs-<label>.yml
// image variant.
func findConfDir() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "pkgs")); err == nil {
			if len(buildcfg.DiscoverRootfsLabels(dir)) > 0 {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// openStore opens the object store at dir, or at the default root if empty. When
// GLBX_REGISTRY names a registry, a pull-through store is composed over the local
// one so a cold cache is filled from the registry on demand.
func openStore(dir string) (*objstore.Store, error) {
	local, err := objstore.NewLocal(dir)
	if err != nil {
		return nil, err
	}
	ref, insecure := registryFromEnv()
	if ref == "" {
		return local, nil
	}
	registry, err := objstore.NewRegistry(ref, insecure)
	if err != nil {
		return nil, err
	}
	return objstore.NewPullThrough(local, registry), nil
}

// resolveCacheDir resolves the object-store directory a command uses: dir, or
// the default root when dir is empty. Commands log this rather than asking the
// store for a path it was handed.
func resolveCacheDir(dir string) string {
	if dir == "" {
		return objstore.DefaultRoot()
	}
	return dir
}

// scratchDir returns the source-build scratch root for the cache directory dir:
// a tmp/ subdirectory of the resolved cache root, so build scratch spills to the
// same disk as the cache rather than to RAM.
func scratchDir(dir string) string {
	return filepath.Join(resolveCacheDir(dir), "tmp")
}

// registryFromEnv reads the remote registry configuration from the environment:
// GLBX_REGISTRY is the "host[:port]/repo" reference and GLBX_REGISTRY_INSECURE
// selects plaintext HTTP for a local test registry.
//
// When GLBX_REGISTRY is present at all — even set to an empty string — its value
// is used verbatim, so an explicit empty value is an explicit "no remote" that
// overrides the default. Only when the variable is entirely absent is the
// default derived from the working tree's github.com origin
// (autoRegistryFromGit); that derivation fails silently to "" (no remote), the
// prior behavior.
func registryFromEnv() (ref string, insecure bool) {
	insecure = os.Getenv("GLBX_REGISTRY_INSECURE") != ""
	if v, ok := os.LookupEnv("GLBX_REGISTRY"); ok {
		return v, insecure
	}
	return autoRegistryFromGit(), insecure
}

// autoRegistryFromGit derives the default registry from the working tree's
// origin remote: for a git checkout whose origin is on github.com it returns
// "ghcr.io/<owner>/<repo>/objstore", the package registry co-located with the
// GitHub repo. Every failure mode — no working tree, git not installed, origin
// absent or unparseable, or an origin host other than github.com — yields ""
// (no remote) silently, so the auto default never turns a missing or foreign
// remote into an error.
func autoRegistryFromGit() string {
	conf := findConfDir()
	if conf == "" {
		return ""
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return ""
	}
	out, err := exec.Command(gitPath, "-C", conf, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	owner, repo, ok := parseGitHubRemote(strings.TrimSpace(string(out)))
	if !ok {
		return ""
	}
	return "ghcr.io/" + owner + "/" + repo + "/objstore"
}

// parseGitHubRemote extracts the owner and repository from a git remote URL that
// points at github.com, accepting the forms git produces for either transport:
//
//	git@github.com:owner/repo.git            (scp-like ssh)
//	ssh://git@github.com/owner/repo.git      (ssh URL)
//	https://github.com/owner/repo.git        (https, optional userinfo)
//	git://github.com/owner/repo.git          (git protocol)
//
// A trailing ".git" and any ".../" path depth beyond owner/repo are tolerated.
// ok is false — and the caller falls back to no remote — when the host is not
// exactly github.com or the owner/repo cannot be read, so only a genuine
// github.com origin selects the auto default.
func parseGitHubRemote(remote string) (owner, repo string, ok bool) {
	const host = "github.com"

	var path string
	switch {
	case strings.HasPrefix(remote, "http://"), strings.HasPrefix(remote, "https://"),
		strings.HasPrefix(remote, "ssh://"), strings.HasPrefix(remote, "git://"):
		u, err := url.Parse(remote)
		if err != nil || u.Hostname() != host {
			return "", "", false
		}
		path = u.Path
	default:
		// scp-like syntax "[user@]host:path"; the first colon splits host from
		// path and there is no "//" authority. Reject a "//" to avoid mistaking a
		// URL whose scheme we did not list for scp-like.
		hostPart, p, found := strings.Cut(remote, ":")
		if !found || strings.Contains(remote, "//") {
			return "", "", false
		}
		if i := strings.LastIndexByte(hostPart, '@'); i >= 0 {
			hostPart = hostPart[i+1:]
		}
		if hostPart != host {
			return "", "", false
		}
		path = p
	}

	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	segs := strings.Split(path, "/")
	if len(segs) != 2 || segs[0] == "" || segs[1] == "" {
		return "", "", false
	}
	return segs[0], segs[1], true
}

// rootContext returns a context carrying a console log target and a logger for
// the given component.
func rootContext(c log.Component) (context.Context, *log.Logger) {
	ctx := log.WithTarget(context.Background(), log.NewConsoleTarget())
	return ctx, log.From(ctx, c)
}

// rootContextStderr is like rootContext but routes every level to stderr, for
// commands whose stdout carries machine-parseable output (e.g. resolve).
func rootContextStderr(c log.Component) (context.Context, *log.Logger) {
	ctx := log.WithTarget(context.Background(), log.NewStderrConsoleTarget())
	return ctx, log.From(ctx, c)
}
