package main

import (
	"flag"
	"fmt"

	"github.com/gardenlinux/glbx/internal/importer"
	"github.com/gardenlinux/glbx/internal/log"
)

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	repo := fs.String("repo", "https://deb.debian.org/debian", "APT repository URL")
	dist := fs.String("dist", "testing", "distribution")
	keyring := fs.String("keyring", "/usr/share/keyrings/debian-archive-keyring.gpg", "GPG keyring path")
	cacheDir := fs.String("cache", "", "object-store cache directory")
	outputDir := fs.String("output", ".", "working-tree root to import into")
	noVerify := fs.Bool("no-verify", false, "skip GPG signature verification")
	cookie := fs.String("cookie", "", "InRelease cache cookie (reuse a cached InRelease within a session)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: glbx import [flags] <package-name>")
	}
	pkgName := fs.Arg(0)

	store, err := openStore(*cacheDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	ctx, l := rootContext(log.Importer)

	result, err := importer.Import(importer.ImportConfig{
		Ctx:       ctx,
		Store:     store,
		RepoURL:   *repo,
		Dist:      *dist,
		Keyring:   *keyring,
		OutputDir: *outputDir,
		NoVerify:  *noVerify,
		Cookie:    *cookie,
	}, pkgName)
	if err != nil {
		return err
	}

	l.Info("imported %s %s (format: %s)", result.Name, result.Version, result.Format)
	for _, s := range result.Sources {
		l.Info("orig: %s (%s)", s.Name, s.Hash)
	}
	return nil
}
