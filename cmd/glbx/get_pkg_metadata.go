package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/gardenlinux/glbx/internal/importer"
)

func cmdGetPkgMetadata(args []string) error {
	fs := flag.NewFlagSet("get-pkg-metadata", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: glbx get-pkg-metadata [flags]")
		fmt.Fprintln(os.Stderr, "\nPrint the most recent import metadata for each package on its upstream")
		fmt.Fprintln(os.Stderr, "lineage reachable from HEAD.")
		fmt.Fprintln(os.Stderr, "\nflags:")
		fs.PrintDefaults()
	}
	confDir := fs.String("conf-dir", "", "configuration directory")
	format := fs.String("format", "yaml", "output format: yaml or json")
	fs.Parse(args)

	if *format != "yaml" && *format != "json" {
		return fmt.Errorf("unknown format %q (want yaml or json)", *format)
	}

	confRoot := *confDir
	if confRoot == "" {
		confRoot = findConfDir()
	}
	if confRoot == "" {
		return fmt.Errorf("cannot find configuration directory; use --conf-dir")
	}

	entries, err := importer.CollectPkgMetadata(confRoot)
	if err != nil {
		return err
	}
	if entries == nil {
		// Marshal an empty array rather than a null when nothing was found.
		entries = []importer.PkgMetadata{}
	}

	var data []byte
	if *format == "json" {
		data, err = json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal metadata: %w", err)
		}
		data = append(data, '\n')
	} else {
		data, err = yaml.Marshal(entries)
		if err != nil {
			return fmt.Errorf("marshal metadata: %w", err)
		}
	}
	fmt.Print(string(data))
	return nil
}
