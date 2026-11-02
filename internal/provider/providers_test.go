package provider

import (
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in      string
		wantNS  string
		wantArg string
		wantErr bool
	}{
		{"rootfs", "rootfs", "", false},
		{"pkg:glibc", "pkg", "glibc", false},
		{"debian:linux", "debian", "linux", false},
		{"debian-kernel:linux", "debian-kernel", "linux", false},
		{"", "", "", true},
		{":glibc", "", "", true},
		{"pkg:", "", "", true},
	}
	for _, tc := range cases {
		got, err := ParseTarget(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseTarget(%q): expected error, got %+v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTarget(%q): %v", tc.in, err)
			continue
		}
		if got.Namespace != tc.wantNS || got.Arg != tc.wantArg {
			t.Errorf("ParseTarget(%q) = {%q,%q}, want {%q,%q}", tc.in, got.Namespace, got.Arg, tc.wantNS, tc.wantArg)
		}
	}
}

func TestParseParams(t *testing.T) {
	p, err := ParseParams([]string{"repo=http://x,dist=testing", "arch=amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if p["repo"] != "http://x" || p["dist"] != "testing" || p["arch"] != "amd64" {
		t.Fatalf("unexpected params: %+v", p)
	}

	// Later assignment wins.
	p, err = ParseParams([]string{"dist=testing", "dist=sid"})
	if err != nil {
		t.Fatal(err)
	}
	if p["dist"] != "sid" {
		t.Fatalf("later assignment should win: %+v", p)
	}

	// Empty fragments are skipped.
	if p, err := ParseParams([]string{"", "a=b"}); err != nil || len(p) != 1 {
		t.Fatalf("empty fragment handling: %+v, %v", p, err)
	}

	for _, bad := range []string{"noequals", "=noval", "a=b,bad"} {
		if _, err := ParseParams([]string{bad}); err == nil {
			t.Errorf("ParseParams(%q): expected error", bad)
		}
	}
}

func TestDebianRepoConfigDefaults(t *testing.T) {
	cfg, err := debianRepoConfig(Params{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo != "https://deb.debian.org/debian" {
		t.Errorf("default repo: %q", cfg.Repo)
	}
	if cfg.Dist != "testing" {
		t.Errorf("default dist: %q", cfg.Dist)
	}
	if cfg.Snapshot == "" {
		t.Error("default snapshot should be set")
	}
	if cfg.Arch == "" {
		t.Error("default arch should be the host arch")
	}
	// Gen* default to the resolved base.
	if cfg.GenRepo != cfg.Repo || cfg.GenDist != cfg.Dist || cfg.GenArch != cfg.Arch {
		t.Errorf("gen defaults should mirror base: %+v", cfg)
	}
}

func TestDebianRepoConfigOverrides(t *testing.T) {
	cfg, err := debianRepoConfig(Params{
		"repo": "http://snap", "dist": "sid", "arch": "arm64",
		"no-verify": "true", "gen-arch": "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo != "http://snap" || cfg.Dist != "sid" || cfg.Arch != "arm64" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if !cfg.NoVerify {
		t.Error("no-verify=true should set NoVerify")
	}
	if cfg.GenArch != "amd64" {
		t.Errorf("gen-arch override: %q", cfg.GenArch)
	}
	// gen-repo/gen-dist still default to base.
	if cfg.GenRepo != "http://snap" || cfg.GenDist != "sid" {
		t.Errorf("gen defaults: %+v", cfg)
	}
}

func TestDebianRepoConfigRejectsUnknownKey(t *testing.T) {
	if _, err := debianRepoConfig(Params{"bogus": "1"}); err == nil {
		t.Fatal("expected an error for an unknown param key")
	}
	if _, err := debianRepoConfig(Params{"no-verify": "maybe"}); err == nil {
		t.Fatal("expected an error for a non-boolean no-verify")
	}
}

func TestLookupLockGeneratorRegistered(t *testing.T) {
	for _, ns := range []string{"debian", "rootfs", "debian-kernel", "pkg"} {
		if _, err := LookupLockGenerator(ns); err != nil {
			t.Errorf("lock generator %q should be registered: %v", ns, err)
		}
	}
	if _, err := LookupLockGenerator("nope"); err == nil {
		t.Error("expected error for an unregistered namespace")
	}
}

func TestLookupImporterRegistered(t *testing.T) {
	if _, err := LookupImporter("debian"); err != nil {
		t.Errorf("debian importer should be registered: %v", err)
	}
	if _, err := LookupImporter("pkg"); err == nil {
		t.Error("there is no pkg importer; lookup should fail")
	}
}

func TestPkgParamsHelpComposesDelegates(t *testing.T) {
	help := pkgForward{}.ParamsHelp()
	if !strings.Contains(help, "pkg_type: debian\n") {
		t.Errorf("composed help missing debian heading:\n%s", help)
	}
	if !strings.Contains(help, "pkg_type: debian-kernel\n") {
		t.Errorf("composed help missing debian-kernel heading:\n%s", help)
	}
	// debian heading comes before debian-kernel heading (pkgDelegates order).
	if strings.Index(help, "pkg_type: debian\n") > strings.Index(help, "pkg_type: debian-kernel\n") {
		t.Error("debian heading should precede debian-kernel heading")
	}
}

func TestPkgForwardRequiresPkgType(t *testing.T) {
	dir := t.TempDir()
	// No pkgs/<name>/ at all: LoadBuildYML returns the zero value, PkgType "".
	err := pkgForward{}.Generate(OpContext{OutputDir: dir}, "glibc")
	if err == nil || !strings.Contains(err.Error(), "pkg_type") {
		t.Fatalf("expected a pkg_type error, got %v", err)
	}
}
