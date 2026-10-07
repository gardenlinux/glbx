package install

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/gardenlinux/glbx/internal/container"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/lockfile"
	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

func requireStub(t *testing.T) string {
	t.Helper()
	stubPath := os.Getenv("GLBX_EXEC_ENV_STUB")
	if stubPath == "" {
		t.Skip("GLBX_EXEC_ENV_STUB not set; run `make build` and `make test` (the Makefile sets it for you)")
	}
	if _, err := os.Stat(stubPath); err != nil {
		t.Skipf("GLBX_EXEC_ENV_STUB=%q does not exist; run `make build` first", stubPath)
	}
	return stubPath
}

// =============================================================================
// Resolve unit tests — fast, no network.
// =============================================================================

// makeIndex builds an index.Index from synthetic stanzas. Each map[string]string
// is one package stanza ("package", "version", "depends", "essential", ...).
func makeIndex(t *testing.T, stanzas []map[string]string) *index.Index {
	t.Helper()
	var b strings.Builder
	for _, s := range stanzas {
		fmt.Fprintf(&b, "Package: %s\n", s["package"])
		// Architecture defaults to amd64 so tests don't have to spell it out.
		if _, ok := s["architecture"]; !ok {
			b.WriteString("Architecture: amd64\n")
		}
		for k, v := range s {
			if k == "package" {
				continue
			}
			fmt.Fprintf(&b, "%s: %s\n", strings.Title(k), v) //nolint:staticcheck
		}
		b.WriteString("\n")
	}
	idx, err := index.Load(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("makeIndex: %v", err)
	}
	return idx
}

func TestResolveEmptyNames(t *testing.T) {
	idx := makeIndex(t, []map[string]string{
		{"package": "libc6", "version": "1"},
	})
	got, err := Resolve(idx, "amd64", nil)
	if err != nil {
		t.Fatalf("Resolve(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no packages for empty roots, got %d", len(got))
	}
}

func TestResolveSingle(t *testing.T) {
	idx := makeIndex(t, []map[string]string{
		{"package": "libc6", "version": "1"},
	})
	got, err := Resolve(idx, "amd64", []string{"libc6"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].Name != "libc6" {
		t.Errorf("expected [libc6], got %v", got)
	}
}

func TestResolveTransitiveDeps(t *testing.T) {
	idx := makeIndex(t, []map[string]string{
		{"package": "libc6", "version": "1"},
		{"package": "zlib1g", "version": "1", "depends": "libc6"},
		{"package": "openssl", "version": "1", "depends": "libc6, zlib1g"},
	})
	got, err := Resolve(idx, "amd64", []string{"openssl"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	names := make(map[string]bool)
	for _, p := range got {
		names[p.Name] = true
	}
	for _, want := range []string{"openssl", "libc6", "zlib1g"} {
		if !names[want] {
			t.Errorf("expected %q in resolved set: %v", want, names)
		}
	}
}

func TestResolveMissingPackage(t *testing.T) {
	idx := makeIndex(t, []map[string]string{
		{"package": "libc6", "version": "1"},
	})
	if _, err := Resolve(idx, "amd64", []string{"does-not-exist"}); err == nil {
		t.Fatal("expected error for unresolvable root")
	}
}

// =============================================================================
// TestBootstrapAndInstallVim — live integration test. Hits deb.debian.org and
// requires user namespaces. Skipped only when its prerequisites are genuinely
// unavailable on the host.
// =============================================================================

func TestBootstrapAndInstallVim(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping bootstrap+install integration test in -short mode")
	}

	ctx := log.WithTarget(context.Background(), log.NewConsoleTarget())

	stubPath := requireStub(t)
	t.Logf("stub: %s", stubPath)

	storeDir := t.TempDir()
	if envDir := os.Getenv("GL_CACHE_DIR"); envDir != "" {
		storeDir = envDir
		os.MkdirAll(storeDir, 0755)
	}
	store, err := objstore.NewLocal(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Logf("store: %s", storeDir)

	t.Log("fetching Debian testing index...")
	idx, err := lockfile.FetchBinaryIndex(ctx, store, "https://deb.debian.org/debian", "testing", "amd64", "bootstrap-test")
	if err != nil {
		// No network / DNS / repo unavailable — skip rather than fail. Other
		// packages exercise FetchBinaryIndex with a httptest server already.
		t.Skipf("cannot reach Debian repo (network required for this integration test): %v", err)
	}
	t.Logf("index: %d packages", idx.Len())

	var coreNames []string
	for _, pkg := range idx.EssentialPackages() {
		coreNames = append(coreNames, pkg.Name)
	}
	t.Logf("essential packages: %d", len(coreNames))

	corePkgs, err := Resolve(idx, "amd64", coreNames)
	if err != nil {
		t.Fatalf("resolve core: %v", err)
	}
	t.Logf("resolved core set: %d packages", len(corePkgs))

	t.Log("downloading core .debs...")
	if err := lockfile.FetchDebs(ctx, store, "https://deb.debian.org/debian", corePkgs); err != nil {
		t.Fatalf("fetch core debs: %v", err)
	}

	base := container.NewBaseExecEnv()
	defer base.Close()

	userNS, err := container.NewUserNS(container.UserNSConfig{
		Ctx:      ctx,
		Parent:   base,
		StubPath: stubPath,
		IDCount:  65536,
	})
	if err != nil {
		// Most likely cause: kernel.unprivileged_userns_clone=0 or seccomp
		// blocks unshare(). Treat as environmental, not a test failure.
		t.Skipf("user namespaces unavailable on this host: %v", err)
	}
	defer userNS.Close()

	mountNS, err := container.NewMountNS(container.MountNSConfig{
		Ctx:      ctx,
		Parent:   userNS,
		StubPath: stubPath,
	})
	if err != nil {
		t.Fatalf("create mountNS: %v", err)
	}
	defer mountNS.Close()

	mountNS.Mkdir("/tmp", 01777)

	t.Log("bootstrapping container...")
	cont, rootfsPath, cleanup, err := Bootstrap(ctx, mountNS, store, idx, "amd64", stubPath)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		// Real failure: the bootstrap is the test. No silent-pass.
		t.Fatalf("Bootstrap failed (rootfs=%s): %v", rootfsPath, err)
	}

	t.Log("bootstrap succeeded; installing vim...")

	vimPkgs, err := Resolve(idx, "amd64", []string{"vim"})
	if err != nil {
		t.Fatalf("resolve vim: %v", err)
	}
	t.Logf("vim resolved to %d packages", len(vimPkgs))

	if err := lockfile.FetchDebs(ctx, store, "https://deb.debian.org/debian", vimPkgs); err != nil {
		t.Fatalf("fetch vim debs: %v", err)
	}

	if err := InstallResolved(ctx, cont, mountNS, store, rootfsPath, vimPkgs); err != nil {
		t.Fatalf("InstallResolved(vim): %v", err)
	}

	t.Log("SUCCESS: vim installed into bootstrapped container")
}
