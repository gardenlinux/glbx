package build

import (
	"bufio"
	"os"
	"strconv"
	"strings"

	"github.com/gardenlinux/glbx/internal/debian/deb822"
	"github.com/gardenlinux/glbx/internal/debian/index"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// The artifact engine stores each artifact's outputs as a text manifest of
// "<hash> <name>\n" lines, one per Output. The blob hash of the manifest is
// what gets stored in store.Map under the artifact's identity.
// parsePackageFromManifest finds the control:<name> + <name>.deb pair and
// returns it as an index.Package.
func parsePackageFromManifest(store *objstore.Store, manifestHash objstore.Hash, binaryName string) *index.Package {
	reader, err := store.OpenBlob(manifestHash)
	if err != nil {
		return nil
	}
	defer reader.Close()

	var controlHash, debHash objstore.Hash

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		h, err := objstore.NewHash(parts[0])
		if err != nil {
			continue
		}
		name := parts[1]
		switch name {
		case "control:" + binaryName:
			controlHash = h
		case binaryName + ".deb":
			debHash = h
		}
	}

	if controlHash.IsZero() {
		return nil
	}

	controlReader, err := store.OpenBlob(controlHash)
	if err != nil {
		return nil
	}
	d822Reader := deb822.NewReader(controlReader)
	stanza, err := d822Reader.Next()
	controlReader.Close()
	if err != nil {
		return nil
	}
	pkg, err := index.ParsePackageFromStanza(stanza)
	if err != nil {
		return nil
	}
	if !debHash.IsZero() {
		pkg.SHA256 = debHash.String()
		pkg.Stanza["sha256"] = debHash.String()
		if info, err := os.Stat(store.Blobs.Path(debHash)); err == nil {
			pkg.Size = info.Size()
			pkg.Stanza["size"] = strconv.FormatInt(info.Size(), 10)
		}
	}
	return pkg
}
