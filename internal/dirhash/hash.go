// Package dirhash reduces a directory tree to a single SHA-256 value that
// changes if and only if the tree's build-relevant content changes: file bytes,
// entry names, entry types, and the one executable bit. Owner, group, the rest
// of the permission bits, timestamps, and inode numbers are excluded, so two
// checkouts of the same source produce byte-identical hashes.
//
// Traversal is confined with os.OpenRoot: all file operations stay within the
// opened directory and symlinks cannot escape it.
package dirhash

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Entry type bytes. These values are part of the hash definition: changing them
// changes every dirhash.
const (
	TypeRegular    byte = 0
	TypeExecutable byte = 1
	TypeDirectory  byte = 2
	TypeSymlink    byte = 3
	TypeSpecial    byte = 4
)

// HashDirectory computes the dirhash of the tree rooted at path. Each directory
// is hashed over its immediate entries in byte-wise name order; for each entry
// the running hash folds in a type byte, the SHA-256 of the entry name, and a
// 32-byte content hash. The content hash is the file's SHA-256 for a regular
// file, the subdirectory's own dirhash for a directory, the SHA-256 of the
// target string for a symlink, and the SHA-256 of the mode type for anything
// else. The result is the root directory's hash as lower-case hex.
func HashDirectory(path string) (string, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return "", fmt.Errorf("dirhash: open root %q: %w", path, err)
	}
	defer root.Close()

	return hashDir(root, path, ".")
}

// hashDir recursively hashes the directory at relPath within root. rootPath is
// the absolute path to the root, needed for reading symlink targets.
func hashDir(root *os.Root, rootPath string, relPath string) (string, error) {
	entries, err := readDirSorted(root, relPath)
	if err != nil {
		return "", err
	}

	h := sha256.New()

	for _, entry := range entries {
		name := entry.Name()
		entryPath := joinPath(relPath, name)

		info, err := root.Lstat(entryPath)
		if err != nil {
			return "", fmt.Errorf("dirhash: lstat %q: %w", entryPath, err)
		}

		var typeByte byte
		var contentHash []byte

		mode := info.Mode()
		switch {
		case mode.IsRegular():
			typeByte = TypeRegular
			if mode&0100 != 0 { // user-execute bit
				typeByte = TypeExecutable
			}
			contentHash, err = hashFile(root, entryPath)
			if err != nil {
				return "", err
			}

		case mode.IsDir():
			typeByte = TypeDirectory
			hexHash, err := hashDir(root, rootPath, entryPath)
			if err != nil {
				return "", err
			}
			contentHash, err = hex.DecodeString(hexHash)
			if err != nil {
				return "", fmt.Errorf("dirhash: decode subtree hash: %w", err)
			}

		case mode&os.ModeSymlink != 0:
			typeByte = TypeSymlink
			contentHash, err = hashSymlink(rootPath, entryPath)
			if err != nil {
				return "", err
			}

		default:
			typeByte = TypeSpecial
			d := sha256.Sum256([]byte(mode.Type().String()))
			contentHash = d[:]
		}

		h.Write([]byte{typeByte})
		nameDigest := sha256.Sum256([]byte(name))
		h.Write(nameDigest[:])
		h.Write(contentHash)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// readDirSorted reads directory entries sorted by name, byte-wise.
func readDirSorted(root *os.Root, relPath string) ([]os.DirEntry, error) {
	f, err := root.Open(relPath)
	if err != nil {
		return nil, fmt.Errorf("dirhash: open dir %q: %w", relPath, err)
	}
	defer f.Close()

	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("dirhash: read dir %q: %w", relPath, err)
	}

	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})

	return entries, nil
}

// hashFile computes the SHA-256 of a regular file's content.
func hashFile(root *os.Root, relPath string) ([]byte, error) {
	f, err := root.Open(relPath)
	if err != nil {
		return nil, fmt.Errorf("dirhash: open file %q: %w", relPath, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, fmt.Errorf("dirhash: read file %q: %w", relPath, err)
	}

	return h.Sum(nil), nil
}

// hashSymlink computes the SHA-256 of a symlink's target string. os.Root has no
// Readlink, so the full path is used; the entry is already known to be a
// symlink from Lstat, and only the target string is read — never followed.
func hashSymlink(rootPath, relPath string) ([]byte, error) {
	fullPath := filepath.Join(rootPath, relPath)
	target, err := os.Readlink(fullPath)
	if err != nil {
		return nil, fmt.Errorf("dirhash: readlink %q: %w", relPath, err)
	}

	digest := sha256.Sum256([]byte(target))
	return digest[:], nil
}

// joinPath joins a relative base path with a child name.
func joinPath(base, name string) string {
	if base == "." {
		return name
	}
	return base + "/" + name
}
