package importer

import (
	"fmt"

	"github.com/gardenlinux/glbx/internal/debian/version"
	"github.com/gardenlinux/glbx/internal/log"
)

// CheckConfig configures an update check: which repo/dist to resolve the newest
// version from, the working tree whose pinned versions are compared against it,
// and the optional filters that narrow which packages are considered.
type CheckConfig struct {
	Import ImportConfig // Store, RepoURL, Dist, Keyring, NoVerify, Cookie, HTTPClient, Ctx

	// ConfDir is the working-tree root holding pkgs/ and the import lineage.
	ConfDir string

	// UpdateTag, when set, keeps only packages whose recorded auto_update equals
	// it, so a check for one logical series ignores packages tracking another.
	UpdateTag string

	// Only, when non-empty, restricts the check to these package names
	// (intersected with the packages present under pkgs/).
	Only []string
}

// Update is one package whose newest available version is higher than the
// version currently pinned on its lineage.
type Update struct {
	Pkg string `json:"pkg"`
	Old string `json:"old"`
	New string `json:"new"`
}

// CheckUpdates reports, for each present package (optionally filtered by update
// tag and by an explicit name list), whether the version an import would select
// from the configured repo/dist is newer than the version currently pinned on
// the package's lineage. The Sources index is loaded once and queried for every
// candidate, so checking many packages costs a single archive-metadata fetch.
// Results are returned in the order packages are discovered in the lineage.
func CheckUpdates(cfg CheckConfig) ([]Update, error) {
	l := log.From(cfg.Import.Ctx, log.Importer)

	pinned, err := CollectPkgMetadata(cfg.ConfDir)
	if err != nil {
		return nil, fmt.Errorf("importer: collecting pinned versions: %w", err)
	}

	only := map[string]bool{}
	for _, name := range cfg.Only {
		only[name] = true
	}

	// Narrow the candidate set before touching the network: by recorded tag and
	// by the explicit name list, so an empty candidate set skips the index load.
	var candidates []PkgMetadata
	for _, p := range pinned {
		if cfg.UpdateTag != "" && p.AutoUpdate != cfg.UpdateTag {
			continue
		}
		if len(only) > 0 && !only[p.Pkg] {
			continue
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	idx, err := loadSourceIndex(cfg.Import)
	if err != nil {
		return nil, err
	}

	var updates []Update
	for _, p := range candidates {
		latest, err := idx.highest(p.Pkg)
		if err != nil {
			// A pinned package absent from the current archive is not an update,
			// not a hard failure: the archive may have dropped or renamed it.
			l.Warn("%s: %s", p.Pkg, err)
			continue
		}
		if version.Compare(latest.Version, p.Version) > 0 {
			updates = append(updates, Update{Pkg: p.Pkg, Old: p.Version, New: latest.Version})
		}
	}
	return updates, nil
}
