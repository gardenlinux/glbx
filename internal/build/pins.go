package build

import (
	"bytes"
	"fmt"
	"os/exec"

	"github.com/gardenlinux/glbx/internal/buildcfg"
	"github.com/gardenlinux/glbx/internal/debian/deb822"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// pinnedToolingIndex builds a package index from the pinned tooling, choosing
// each tool's architecture-matching (or "all") file. Each package's full
// control stanza — Depends, Pre-Depends, Provides, Essential, Priority — is read
// from its .deb in the store, so the index supports real dependency resolution
// and essential-set selection, not just name/version lookup. The .deb's content
// hash is recorded as the package's SHA256 so install steps can bind the blob.
func pinnedToolingIndex(store *objstore.Store, tools []buildcfg.PinnedTool, arch string) (*index.Index, error) {
	idx := index.New()
	for _, tool := range tools {
		for _, f := range tool.Files {
			if f.Arch != arch && f.Arch != "all" {
				continue
			}
			if idx.Get(tool.Name) != nil {
				break
			}
			if err := store.EnsureBlob(f.Hash); err != nil {
				return nil, fmt.Errorf("tool %s: %w", tool.Name, err)
			}
			control, err := debControl(store.Blobs.Path(f.Hash))
			if err != nil {
				return nil, fmt.Errorf("tool %s: read control: %w", tool.Name, err)
			}
			control["sha256"] = f.Hash.String()
			pkg, err := index.ParsePackageFromStanza(control)
			if err != nil {
				return nil, fmt.Errorf("tool %s: %w", tool.Name, err)
			}
			idx.Add(pkg)
			break
		}
	}
	return idx, nil
}

// debControl reads a .deb's control stanza via `dpkg-deb -f`.
func debControl(debPath string) (deb822.Stanza, error) {
	out, err := exec.Command("dpkg-deb", "-f", debPath).Output()
	if err != nil {
		return nil, fmt.Errorf("dpkg-deb -f: %w", err)
	}
	stanza, err := deb822.NewReader(bytes.NewReader(out)).Next()
	if err != nil {
		return nil, fmt.Errorf("parse control: %w", err)
	}
	return stanza, nil
}
