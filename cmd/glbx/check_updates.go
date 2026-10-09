package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/gardenlinux/glbx/internal/importer"
	"github.com/gardenlinux/glbx/internal/log"
)

func cmdCheckUpdates(args []string) error {
	fs := flag.NewFlagSet("check-updates", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: glbx check-updates [flags] [package ...]")
		fmt.Fprintln(os.Stderr, "\nReport packages whose newest version in the configured repo/dist is higher")
		fmt.Fprintln(os.Stderr, "than the version currently pinned on their lineage. With no package arguments")
		fmt.Fprintln(os.Stderr, "every present package is checked. Logs go to stderr; results to stdout.")
		fmt.Fprintln(os.Stderr, "\nflags:")
		fs.PrintDefaults()
	}
	confDir := fs.String("conf-dir", "", "configuration directory")
	repo := fs.String("repo", "https://deb.debian.org/debian", "APT repository URL")
	dist := fs.String("dist", "testing", "distribution")
	updateTag := fs.String("update-tag", "", "only consider packages whose recorded auto_update equals this tag")
	keyring := fs.String("keyring", "/usr/share/keyrings/debian-archive-keyring.gpg", "GPG keyring path")
	cacheDir := fs.String("cache", "", "object-store cache directory")
	noVerify := fs.Bool("no-verify", false, "skip GPG signature verification")
	cookie := fs.String("cookie", "", "InRelease cache cookie (reuse a cached InRelease within a session)")
	format := fs.String("format", "text", "output format: text or json")
	fs.Parse(args)

	if *format != "text" && *format != "json" {
		return fmt.Errorf("unknown format %q (want text or json)", *format)
	}

	confRoot := *confDir
	if confRoot == "" {
		confRoot = findConfDir()
	}
	if confRoot == "" {
		return fmt.Errorf("cannot find configuration directory; use --conf-dir")
	}

	store, err := openStore(*cacheDir)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	ctx, l := rootContextStderr(log.Importer)

	updates, err := importer.CheckUpdates(importer.CheckConfig{
		Import: importer.ImportConfig{
			Ctx:      ctx,
			Store:    store,
			RepoURL:  *repo,
			Dist:     *dist,
			Keyring:  *keyring,
			NoVerify: *noVerify,
			Cookie:   *cookie,
		},
		ConfDir:   confRoot,
		UpdateTag: *updateTag,
		Only:      fs.Args(),
	})
	if err != nil {
		return err
	}

	if *format == "json" {
		if updates == nil {
			updates = []importer.Update{}
		}
		data, err := json.Marshal(updates)
		if err != nil {
			return fmt.Errorf("marshal updates: %w", err)
		}
		fmt.Println(string(data))
	} else {
		for _, u := range updates {
			fmt.Printf("%s %s -> %s\n", u.Pkg, u.Old, u.New)
		}
	}
	l.Info("%d package(s) with a newer version available", len(updates))
	return nil
}
