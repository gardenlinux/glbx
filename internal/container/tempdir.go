package container

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"syscall"
)

// doMkTempDir creates a new directory inside dir whose name is prefix+8hex,
// retrying on EEXIST until it wins the race or a different error occurs. The
// mkdir callback lets callers create the directory on the host or inside a
// namespace.
func doMkTempDir(dir, prefix string, mkdir func(string) error) (string, error) {
	for {
		path := filepath.Join(dir, prefix+randomHex8())
		err := mkdir(path)
		if err == nil {
			return path, nil
		}
		if isEEXIST(err) {
			continue
		}
		return "", err
	}
}

func randomHex8() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// isEEXIST reports whether err represents EEXIST. It handles both local syscall
// errors and errors transported over the control channel, which lose their
// concrete type.
func isEEXIST(err error) bool {
	return errors.Is(err, syscall.EEXIST) || strings.Contains(err.Error(), "file exists")
}
