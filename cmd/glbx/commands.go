package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/provider"
)

// repeatedFlag collects a flag that may be given more than once; the values
// accumulate in order.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }
func (r *repeatedFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

func cmdLockfile(args []string) error {
	fs := flag.NewFlagSet("lockfile", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: glbx lockfile [flags] <target>")
		fmt.Fprintln(os.Stderr, "\nGenerate a pinned dependency lock. <target> is <namespace>:<arg>, or a")
		fmt.Fprintln(os.Stderr, "bare namespace for a zero-argument one:")
		fmt.Fprintln(os.Stderr, "  pkg:<name>            dispatch on the package's build.yml pkg_type")
		fmt.Fprintln(os.Stderr, "  tooling-pkg:<name>    the build-tooling lock for an images.d/tooling/ package")
		fmt.Fprintln(os.Stderr, "  rootfs                the image configuration-tooling lock")
		fmt.Fprintln(os.Stderr, "  image-type:<name>     an image type's assembly-tooling lock")
		fmt.Fprintln(os.Stderr, "  debian:<name>         the plain Debian lock, directly")
		fmt.Fprintln(os.Stderr, "  debian-kernel:<name>  the kernel lock, directly")
		fmt.Fprintln(os.Stderr, "\nflags:")
		fs.PrintDefaults()
	}
	var params repeatedFlag
	fs.Var(&params, "params", "provider parameters as key=val,key=val (repeatable); see --params-help <namespace>")
	paramsHelp := fs.String("params-help", "", "print the parameters a namespace accepts and exit")
	architectures := fs.String("architectures", "amd64,arm64", "comma-separated target architectures to lock, written wholesale in one pass")
	cacheDir := fs.String("cache", "", "object-store cache directory")
	outputDir := fs.String("output", ".", "working-tree root")
	stubPath := fs.String("stub", "", "path to exec_env_stub binary (for container-backed providers)")
	fs.Parse(args)

	if *paramsHelp != "" {
		g, err := provider.LookupLockGenerator(*paramsHelp)
		if err != nil {
			return err
		}
		fmt.Println(g.ParamsHelp())
		return nil
	}

	if fs.NArg() < 1 {
		fs.Usage()
		return fmt.Errorf("missing <target> argument")
	}
	target, err := provider.ParseTarget(fs.Arg(0))
	if err != nil {
		return err
	}
	parsedParams, err := provider.ParseParams(params)
	if err != nil {
		return err
	}
	g, err := provider.LookupLockGenerator(target.Namespace)
	if err != nil {
		return err
	}

	arches, err := parseArches(*architectures)
	if err != nil {
		return err
	}

	store, err := openStore(*cacheDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	ctx, _ := rootContext(log.Lockfile)

	return g.Generate(provider.OpContext{
		Ctx:       ctx,
		Store:     store,
		OutputDir: *outputDir,
		Params:    parsedParams,
		StubPath:  *stubPath,
		Arches:    arches,
	}, target.Arg)
}

// parseArches splits a comma-separated --architectures value into a cleaned,
// non-empty, duplicate-free list, preserving order.
func parseArches(s string) ([]string, error) {
	var out []string
	seen := make(map[string]bool)
	for _, a := range strings.Split(s, ",") {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--architectures must name at least one architecture")
	}
	return out, nil
}

func cmdStatus(args []string) error {
	_, l := rootContext(log.Engine)
	if _, err := openStore(""); err != nil {
		return err
	}
	l.Info("cache: %s", resolveCacheDir(""))
	if ref, _ := registryFromEnv(); ref != "" {
		l.Info("registry: %s", ref)
	} else {
		l.Info("registry: none (local only)")
	}
	if confRoot := findConfDir(); confRoot != "" {
		l.Info("working tree: %s", confRoot)
	} else {
		l.Info("working tree: not found from the current directory")
	}
	return nil
}
