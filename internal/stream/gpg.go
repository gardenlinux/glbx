package stream

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// GPGVerify verifies a detached GPG signature against a data file using gpgv.
// keyringPath is the path to the keyring file (.gpg format).
// signaturePath is the path to the detached signature file.
// dataPath is the path to the signed data file.
// Returns an error if verification fails.
func GPGVerify(keyringPath, signaturePath, dataPath string) error {
	cmd := exec.Command("gpgv", "--keyring", keyringPath, signaturePath, dataPath)

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("stream: gpg verification failed: %w\nstderr: %s", err, stderrBuf.String())
	}
	return nil
}

// GPGVerifyClearSigned verifies a cleartext-signed document (e.g., InRelease)
// and returns the verified payload with the signature envelope stripped.
// keyringPath is the path to the keyring file (.gpg format).
// signedData is the reader containing the cleartext-signed document.
//
// Implementation: writes signed data to a temp file, uses gpgv to verify,
// then strips the PGP cleartext signature framing to extract the payload.
func GPGVerifyClearSigned(keyringPath string, signedData io.Reader) ([]byte, error) {
	// Read all signed data into memory
	data, err := io.ReadAll(signedData)
	if err != nil {
		return nil, fmt.Errorf("stream: reading signed data: %w", err)
	}

	// Write to a temp file for gpgv
	tmpFile, err := os.CreateTemp("", "gpg-clearsigned-*")
	if err != nil {
		return nil, fmt.Errorf("stream: creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("stream: writing temp file: %w", err)
	}
	tmpFile.Close()

	// Verify with gpgv
	cmd := exec.Command("gpgv", "--keyring", keyringPath, tmpPath)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("stream: gpg clearsigned verification failed: %w\nstderr: %s", err, stderrBuf.String())
	}

	// Extract payload from cleartext signature format:
	// -----BEGIN PGP SIGNED MESSAGE-----
	// Hash: SHA256
	// <blank line>
	// <payload>
	// -----BEGIN PGP SIGNATURE-----
	// ...
	// -----END PGP SIGNATURE-----
	payload, err := extractClearSignedPayload(data)
	if err != nil {
		return nil, fmt.Errorf("stream: extracting cleartext payload: %w", err)
	}

	return payload, nil
}

// ExtractClearSignedPayload strips the PGP cleartext signature envelope
// and returns the original signed content, un-dash-escaping as needed.
// This does NOT verify the signature — use GPGVerifyClearSigned for that.
func ExtractClearSignedPayload(data []byte) ([]byte, error) {
	return extractClearSignedPayload(data)
}

// extractClearSignedPayload strips the PGP cleartext signature envelope
// and returns the original signed content, un-dash-escaping as needed.
func extractClearSignedPayload(data []byte) ([]byte, error) {
	content := string(data)

	// Find the start of the signed content (after "Hash: ..." header and blank line)
	beginMarker := "-----BEGIN PGP SIGNED MESSAGE-----"
	sigMarker := "-----BEGIN PGP SIGNATURE-----"

	beginIdx := strings.Index(content, beginMarker)
	if beginIdx < 0 {
		return nil, fmt.Errorf("missing BEGIN PGP SIGNED MESSAGE marker")
	}

	// Skip the header section (everything up to the first blank line after the marker)
	afterBegin := content[beginIdx+len(beginMarker):]
	blankLineIdx := strings.Index(afterBegin, "\n\n")
	if blankLineIdx < 0 {
		// Try with \r\n
		blankLineIdx = strings.Index(afterBegin, "\r\n\r\n")
		if blankLineIdx < 0 {
			return nil, fmt.Errorf("missing blank line after PGP header")
		}
		blankLineIdx += 2 // account for \r\n vs \n
	}
	payloadStart := afterBegin[blankLineIdx+2:] // skip the \n\n

	// Find the start of the signature block
	sigIdx := strings.Index(payloadStart, sigMarker)
	if sigIdx < 0 {
		return nil, fmt.Errorf("missing BEGIN PGP SIGNATURE marker")
	}

	// The payload is everything between the blank line and the signature marker.
	// Remove the trailing newline before the signature marker.
	payload := payloadStart[:sigIdx]
	payload = strings.TrimRight(payload, "\r\n")

	// Un-dash-escape: lines starting with "- " have the "- " prefix removed
	lines := strings.Split(payload, "\n")
	var result []string
	for _, line := range lines {
		if strings.HasPrefix(line, "- ") {
			result = append(result, line[2:])
		} else {
			result = append(result, line)
		}
	}

	return []byte(strings.Join(result, "\n") + "\n"), nil
}
