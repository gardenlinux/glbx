package importer

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gardenlinux/glbx/internal/objstore"
	"github.com/gardenlinux/glbx/internal/stream"
)

// extractDebian extracts the debian/ directory from the source package files
// based on the package's source format.
// files maps filename -> objstore.Hash (as stored in the blob store).
func extractDebian(cfg ImportConfig, pkg *sourcePackage, files map[string]objstore.Hash, pkgDir string) error {
	format := normalizeFormat(pkg.Format)

	switch format {
	case "3.0 (quilt)":
		return extractQuilt(cfg, pkg, files, pkgDir)
	case "3.0 (native)":
		return extractNative(cfg, pkg, files, pkgDir)
	case "1.0":
		return extractLegacy(cfg, pkg, files, pkgDir)
	default:
		return fmt.Errorf("unsupported source format %q", pkg.Format)
	}
}

// normalizeFormat normalizes the Format field value from a Sources stanza.
func normalizeFormat(format string) string {
	format = strings.TrimSpace(format)
	return format
}

// extractQuilt handles 3.0 (quilt) format packages.
// It extracts the .debian.tar.* to get the debian/ directory.
func extractQuilt(cfg ImportConfig, pkg *sourcePackage, files map[string]objstore.Hash, pkgDir string) error {
	// Find the debian tarball
	debianTar := findDebianTar(pkg)
	if debianTar == "" {
		return fmt.Errorf("no .debian.tar.* file found for quilt package %s", pkg.Name)
	}

	hash, ok := files[debianTar]
	if !ok {
		return fmt.Errorf("debian tarball %s not found in downloaded files", debianTar)
	}

	return extractDebianTar(cfg, hash.String(), debianTar, pkgDir)
}

// extractNative handles 3.0 (native) and 1.0 native format packages.
// It extracts the full source tree (not just debian/) since native packages
// contain source files alongside the debian/ directory.
func extractNative(cfg ImportConfig, pkg *sourcePackage, files map[string]objstore.Hash, pkgDir string) error {
	nativeTar := findNativeTar(pkg)
	if nativeTar == "" {
		return fmt.Errorf("no native tarball found for package %s", pkg.Name)
	}

	hash, ok := files[nativeTar]
	if !ok {
		return fmt.Errorf("native tarball %s not found in downloaded files", nativeTar)
	}

	// Extract entire tarball into pkgDir/src/ with strip-components=1
	srcDir := filepath.Join(pkgDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return fmt.Errorf("creating src directory: %w", err)
	}

	if err := extractTarFromStore(cfg, hash.String(), nativeTar, srcDir, 1); err != nil {
		return fmt.Errorf("extracting native tarball: %w", err)
	}

	// Verify debian/ exists in the extracted tree
	if _, err := os.Stat(filepath.Join(srcDir, "debian")); os.IsNotExist(err) {
		return fmt.Errorf("native tarball %s does not contain a debian/ directory", nativeTar)
	}

	return nil
}

// extractLegacy handles 1.0 format packages.
// If there's a .diff.gz, it applies the diff to the orig tarball and extracts debian/.
// If there's no diff, it's treated as a native package.
func extractLegacy(cfg ImportConfig, pkg *sourcePackage, files map[string]objstore.Hash, pkgDir string) error {
	diffFile := findDiffFile(pkg)
	if diffFile == "" {
		// 1.0 native: no diff, treat as native format
		return extractNative(cfg, pkg, files, pkgDir)
	}

	// 1.0 with diff: extract orig, apply diff, get debian/
	return extractLegacyWithDiff(cfg, pkg, files, pkgDir, diffFile)
}

// extractLegacyWithDiff handles 1.0 format with a .diff.gz file.
// It extracts the orig tarball to a/ and b/, applies the diff to b/ via patch(1),
// moves the resulting debian/ directory to the output, and generates a quilt patch
// from any non-debian changes via diff(1).
func extractLegacyWithDiff(cfg ImportConfig, pkg *sourcePackage, files map[string]objstore.Hash, pkgDir string, diffFile string) error {
	// Find the orig tarball
	origTar := findOrigTar(pkg)
	if origTar == "" {
		return fmt.Errorf("no orig tarball found for 1.0 package %s with diff", pkg.Name)
	}

	origHash, ok := files[origTar]
	if !ok {
		return fmt.Errorf("orig tarball %s not found in downloaded files", origTar)
	}

	diffHash, ok := files[diffFile]
	if !ok {
		return fmt.Errorf("diff file %s not found in downloaded files", diffFile)
	}

	// Create temp dir with a/ and b/ subdirectories
	tmpDir, err := os.MkdirTemp("", "glbx-import-1.0-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	dirA := filepath.Join(tmpDir, "a")
	dirB := filepath.Join(tmpDir, "b")
	os.MkdirAll(dirA, 0o755)

	// Extract orig tarball into a/ (strip first component)
	if err := extractTarFromStore(cfg, origHash.String(), origTar, dirA, 1); err != nil {
		return fmt.Errorf("extracting orig tarball: %w", err)
	}

	// Copy a/ -> b/
	if err := copyDir(dirA, dirB); err != nil {
		return fmt.Errorf("copying orig to patched dir: %w", err)
	}

	// Decompress the diff
	diffData, err := readBlobFromStore(cfg, diffHash.String())
	if err != nil {
		return fmt.Errorf("reading diff file: %w", err)
	}

	var decompressedDiff []byte
	if strings.HasSuffix(diffFile, ".gz") {
		decomp, err := stream.GzipDecompress(bytes.NewReader(diffData))
		if err != nil {
			return fmt.Errorf("decompressing diff: %w", err)
		}
		decompressedDiff, err = io.ReadAll(decomp)
		decomp.Close()
		if err != nil {
			return fmt.Errorf("reading decompressed diff: %w", err)
		}
	} else if strings.HasSuffix(diffFile, ".xz") {
		decomp, err := stream.XZDecompress(bytes.NewReader(diffData))
		if err != nil {
			return fmt.Errorf("decompressing diff: %w", err)
		}
		decompressedDiff, err = io.ReadAll(decomp)
		decomp.Close()
		if err != nil {
			return fmt.Errorf("reading decompressed diff: %w", err)
		}
	} else if strings.HasSuffix(diffFile, ".bz2") {
		decomp, err := stream.Bzip2Decompress(bytes.NewReader(diffData))
		if err != nil {
			return fmt.Errorf("decompressing diff: %w", err)
		}
		decompressedDiff, err = io.ReadAll(decomp)
		decomp.Close()
		if err != nil {
			return fmt.Errorf("reading decompressed diff: %w", err)
		}
	} else {
		decompressedDiff = diffData
	}

	// Apply the diff to b/ using patch(1)
	patchCmd := exec.Command("patch", "-p1", "--fuzz=0")
	patchCmd.Dir = dirB
	patchCmd.Stdin = bytes.NewReader(decompressedDiff)
	patchOut, err := patchCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("applying diff with patch -p1: %w\noutput: %s", err, patchOut)
	}

	// Move b/debian/ to the output
	srcDebian := filepath.Join(dirB, "debian")
	dstDebian := filepath.Join(pkgDir, "src", "debian")

	if _, err := os.Stat(srcDebian); os.IsNotExist(err) {
		return fmt.Errorf("diff did not create a debian/ directory")
	}

	if err := copyDir(srcDebian, dstDebian); err != nil {
		return fmt.Errorf("copying debian/ directory: %w", err)
	}

	// Remove debian/ from both a/ and b/ before diffing for non-debian changes
	os.RemoveAll(filepath.Join(dirA, "debian"))
	os.RemoveAll(srcDebian)

	// Generate a patch from non-debian changes using diff(1)
	diffCmd := exec.Command("diff", "-Naur", "a", "b")
	diffCmd.Dir = tmpDir
	patchOutput, err := diffCmd.Output()
	if err != nil {
		// diff returns exit code 1 when files differ (normal), 2+ on error
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() > 1 {
				return fmt.Errorf("generating non-debian patch: %w", err)
			}
		} else {
			return fmt.Errorf("generating non-debian patch: %w", err)
		}
	}

	// Strip wall-clock timestamps from unified-diff headers so the patch is
	// byte-identical across runs. patch(1) ignores the timestamp; only the
	// filename matters.
	patchOutput = stripDiffTimestamps(patchOutput)

	// If there are non-debian changes, save as a quilt patch
	if len(patchOutput) > 0 {
		patchesDir := filepath.Join(dstDebian, "patches")
		if err := os.MkdirAll(patchesDir, 0o755); err != nil {
			return fmt.Errorf("creating patches dir: %w", err)
		}
		patchPath := filepath.Join(patchesDir, "debian.patch")
		if err := os.WriteFile(patchPath, patchOutput, 0o644); err != nil {
			return fmt.Errorf("writing debian.patch: %w", err)
		}
		seriesPath := filepath.Join(patchesDir, "series")
		if err := os.WriteFile(seriesPath, []byte("debian.patch\n"), 0o644); err != nil {
			return fmt.Errorf("writing series file: %w", err)
		}
	}

	return nil
}

// extractDebianTar extracts a .debian.tar.* file into pkgDir/src/.
// The debian tarball typically has a top-level debian/ directory,
// so we extract directly into pkgDir/src/.
func extractDebianTar(cfg ImportConfig, hash string, filename string, pkgDir string) error {
	destDir := filepath.Join(pkgDir, "src")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating src directory: %w", err)
	}

	data, err := readBlobFromStore(cfg, hash)
	if err != nil {
		return fmt.Errorf("reading debian tarball from store: %w", err)
	}

	// Decompress and extract
	decomp, err := stream.AutoDecompress(bytes.NewReader(data), filename)
	if err != nil {
		return fmt.Errorf("decompressing %s: %w", filename, err)
	}
	defer decomp.Close()

	if err := stream.TarExtract(decomp, destDir, 0); err != nil {
		return fmt.Errorf("extracting %s: %w", filename, err)
	}

	return nil
}

// extractTarFromStore reads a tarball from the object store and extracts it.
func extractTarFromStore(cfg ImportConfig, hash string, filename string, targetDir string, stripComponents int) error {
	data, err := readBlobFromStore(cfg, hash)
	if err != nil {
		return fmt.Errorf("reading tarball from store: %w", err)
	}

	if err := stream.TarExtractCompressed(bytes.NewReader(data), filename, targetDir, stripComponents); err != nil {
		return fmt.Errorf("extracting tarball: %w", err)
	}

	return nil
}

// readBlobFromStore reads a blob from the object store by its hash string.
func readBlobFromStore(cfg ImportConfig, hashStr string) ([]byte, error) {
	h, err := objstore.NewHash(hashStr)
	if err != nil {
		return nil, fmt.Errorf("invalid hash %s: %w", hashStr, err)
	}

	rc, err := cfg.Store.Blobs.Open(h)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	return io.ReadAll(rc)
}

// findDebianTar finds the .debian.tar.* file in a source package's file list.
func findDebianTar(pkg *sourcePackage) string {
	for _, f := range pkg.Files {
		if strings.Contains(f.Name, ".debian.tar.") {
			return f.Name
		}
	}
	return ""
}

// findNativeTar finds the native tarball (not orig, not diff) in a source package's file list.
func findNativeTar(pkg *sourcePackage) string {
	for _, f := range pkg.Files {
		if !isOrigTarball(f.Name) && !isDiffFile(f.Name) && isTarball(f.Name) {
			return f.Name
		}
	}
	return ""
}

// findOrigTar finds the primary .orig.tar.* file in a source package's file list.
func findOrigTar(pkg *sourcePackage) string {
	for _, f := range pkg.Files {
		if strings.Contains(f.Name, ".orig.tar.") && !strings.Contains(f.Name, ".orig-") {
			return f.Name
		}
	}
	return ""
}

// findDiffFile finds the .diff.gz (or similar) file in a source package's file list.
func findDiffFile(pkg *sourcePackage) string {
	for _, f := range pkg.Files {
		if isDiffFile(f.Name) {
			return f.Name
		}
	}
	return ""
}

// isDiffFile reports whether a filename is a Debian diff file.
func isDiffFile(name string) bool {
	return strings.Contains(name, ".diff.gz") ||
		strings.Contains(name, ".diff.xz") ||
		strings.Contains(name, ".diff.bz2")
}

// isTarball reports whether a filename looks like a tarball.
func isTarball(name string) bool {
	return strings.Contains(name, ".tar.")
}

// stripDiffTimestamps removes the timestamp suffix from unified-diff header
// lines ("--- path\ttimestamp" / "+++ path\ttimestamp"). The timestamp is
// optional in the unified-diff format and patch(1) ignores it; dropping it
// makes the generated patch deterministic across runs.
func stripDiffTimestamps(patch []byte) []byte {
	lines := bytes.Split(patch, []byte("\n"))
	for i, line := range lines {
		if !bytes.HasPrefix(line, []byte("--- ")) && !bytes.HasPrefix(line, []byte("+++ ")) {
			continue
		}
		if tab := bytes.IndexByte(line, '\t'); tab >= 0 {
			lines[i] = line[:tab]
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

// copyDir recursively copies a directory tree.
func copyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else if entry.Type()&os.ModeSymlink != 0 {
			// Copy symlinks
			target, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// copyFile copies a single file.
func copyFile(src, dst string) error {
	srcF, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcF.Close()

	srcInfo, err := srcF.Stat()
	if err != nil {
		return err
	}

	dstF, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer dstF.Close()

	_, err = io.Copy(dstF, srcF)
	return err
}
