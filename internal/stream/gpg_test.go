package stream

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupGPGKeyring creates a temporary GPG keyring for testing.
// Returns the keyring path and the home directory (for cleanup).
func setupGPGKeyring(t *testing.T) (keyringPath string, gpgHome string) {
	t.Helper()

	gpgHome = t.TempDir()
	keyringPath = filepath.Join(gpgHome, "test-keyring.gpg")

	// Generate a test key (batch mode, no passphrase)
	keyGenScript := `%no-protection
Key-Type: RSA
Key-Length: 2048
Name-Real: Test User
Name-Email: test@example.com
Expire-Date: 0
%commit
`
	cmd := exec.Command("gpg", "--batch", "--gen-key", "--homedir", gpgHome)
	cmd.Stdin = strings.NewReader(keyGenScript)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generating test key: %v\nstderr: %s", err, stderr.String())
	}

	// Export the public key to a keyring file
	cmd = exec.Command("gpg", "--homedir", gpgHome, "--no-default-keyring",
		"--export", "--output", keyringPath)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("exporting key: %v\nstderr: %s", err, stderr.String())
	}

	return keyringPath, gpgHome
}

func TestGPGVerify_DetachedSignature(t *testing.T) {
	keyringPath, gpgHome := setupGPGKeyring(t)

	// Create a test file and sign it
	dataFile := filepath.Join(t.TempDir(), "data.txt")
	if err := os.WriteFile(dataFile, []byte("test data for GPG verification\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sigFile := dataFile + ".sig"
	cmd := exec.Command("gpg", "--homedir", gpgHome, "--batch", "--yes",
		"--detach-sign", "--output", sigFile, dataFile)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("signing: %v\nstderr: %s", err, stderr.String())
	}

	// Verify the signature
	if err := GPGVerify(keyringPath, sigFile, dataFile); err != nil {
		t.Fatalf("GPGVerify: %v", err)
	}
}

func TestGPGVerify_TamperedData(t *testing.T) {
	keyringPath, gpgHome := setupGPGKeyring(t)

	// Create and sign a file
	dataFile := filepath.Join(t.TempDir(), "data.txt")
	if err := os.WriteFile(dataFile, []byte("original content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sigFile := dataFile + ".sig"
	cmd := exec.Command("gpg", "--homedir", gpgHome, "--batch", "--yes",
		"--detach-sign", "--output", sigFile, dataFile)
	if err := cmd.Run(); err != nil {
		t.Fatalf("signing: %v", err)
	}

	// Tamper with the data
	if err := os.WriteFile(dataFile, []byte("TAMPERED content\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Verification should fail
	err := GPGVerify(keyringPath, sigFile, dataFile)
	if err == nil {
		t.Fatal("expected verification failure for tampered data")
	}
}

func TestGPGVerify_InvalidKeyring(t *testing.T) {
	// Use a nonexistent keyring
	dataFile := filepath.Join(t.TempDir(), "data.txt")
	if err := os.WriteFile(dataFile, []byte("test\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err := GPGVerify("/nonexistent/keyring.gpg", "/nonexistent/sig", dataFile)
	if err == nil {
		t.Fatal("expected error with nonexistent keyring")
	}
}

func TestGPGVerifyClearSigned(t *testing.T) {
	keyringPath, gpgHome := setupGPGKeyring(t)

	// Create cleartext-signed data (like InRelease)
	originalData := "Package: test\nVersion: 1.0\nDescription: A test package\n"
	dataFile := filepath.Join(t.TempDir(), "data.txt")
	if err := os.WriteFile(dataFile, []byte(originalData), 0644); err != nil {
		t.Fatal(err)
	}

	// Create cleartext signature
	signedFile := filepath.Join(t.TempDir(), "data.txt.asc")
	cmd := exec.Command("gpg", "--homedir", gpgHome, "--batch", "--yes",
		"--clearsign", "--output", signedFile, dataFile)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("clearsigning: %v\nstderr: %s", err, stderr.String())
	}

	// Read the signed file
	signedData, err := os.ReadFile(signedFile)
	if err != nil {
		t.Fatal(err)
	}

	// Verify and extract payload
	payload, err := GPGVerifyClearSigned(keyringPath, bytes.NewReader(signedData))
	if err != nil {
		t.Fatalf("GPGVerifyClearSigned: %v", err)
	}

	if string(payload) != originalData {
		t.Errorf("payload = %q, want %q", string(payload), originalData)
	}
}

func TestGPGVerifyClearSigned_TamperedContent(t *testing.T) {
	keyringPath, gpgHome := setupGPGKeyring(t)

	// Create and sign data
	dataFile := filepath.Join(t.TempDir(), "data.txt")
	if err := os.WriteFile(dataFile, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}

	signedFile := filepath.Join(t.TempDir(), "data.txt.asc")
	cmd := exec.Command("gpg", "--homedir", gpgHome, "--batch", "--yes",
		"--clearsign", "--output", signedFile, dataFile)
	if err := cmd.Run(); err != nil {
		t.Fatalf("clearsigning: %v", err)
	}

	// Read and tamper with the signed content
	signedData, err := os.ReadFile(signedFile)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(signedData, []byte("original"), []byte("TAMPERED"), 1)

	// Verification should fail
	_, err = GPGVerifyClearSigned(keyringPath, bytes.NewReader(tampered))
	if err == nil {
		t.Fatal("expected verification failure for tampered cleartext")
	}
}

func TestGPGVerifyClearSigned_DashEscaping(t *testing.T) {
	keyringPath, gpgHome := setupGPGKeyring(t)

	// Create data that triggers dash-escaping (lines starting with "-----" or "- ")
	// GPG dash-escapes lines starting with '-' in cleartext signatures
	originalData := "Normal line\n- Dash line that gets escaped\n--also-escaped\nEnd\n"
	dataFile := filepath.Join(t.TempDir(), "data.txt")
	if err := os.WriteFile(dataFile, []byte(originalData), 0644); err != nil {
		t.Fatal(err)
	}

	signedFile := filepath.Join(t.TempDir(), "data.txt.asc")
	cmd := exec.Command("gpg", "--homedir", gpgHome, "--batch", "--yes",
		"--clearsign", "--output", signedFile, dataFile)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("clearsigning: %v\nstderr: %s", err, stderr.String())
	}

	signedData, err := os.ReadFile(signedFile)
	if err != nil {
		t.Fatal(err)
	}

	payload, err := GPGVerifyClearSigned(keyringPath, bytes.NewReader(signedData))
	if err != nil {
		t.Fatalf("GPGVerifyClearSigned: %v", err)
	}

	if string(payload) != originalData {
		t.Errorf("payload = %q, want %q", string(payload), originalData)
	}
}

func TestExtractClearSignedPayload_MissingMarkers(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"no markers", "just some text"},
		{"no begin", "-----BEGIN PGP SIGNATURE-----\nblah\n-----END PGP SIGNATURE-----\n"},
		{"no sig", "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA256\n\npayload\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := extractClearSignedPayload([]byte(tt.data))
			if err == nil {
				t.Fatal("expected error for malformed cleartext")
			}
		})
	}
}

func TestExtractClearSignedPayload_Valid(t *testing.T) {
	signed := `-----BEGIN PGP SIGNED MESSAGE-----
Hash: SHA256

Hello, World!
This is the payload.
-----BEGIN PGP SIGNATURE-----

iQEzBAEBCAAdFiEE...
-----END PGP SIGNATURE-----
`
	payload, err := extractClearSignedPayload([]byte(signed))
	if err != nil {
		t.Fatalf("extractClearSignedPayload: %v", err)
	}

	expected := "Hello, World!\nThis is the payload.\n"
	if string(payload) != expected {
		t.Errorf("got %q, want %q", string(payload), expected)
	}
}

func TestExtractClearSignedPayload_DashEscaped(t *testing.T) {
	signed := `-----BEGIN PGP SIGNED MESSAGE-----
Hash: SHA256

Normal line
- - Escaped dash line
- -----ESCAPED MARKER-----
-----BEGIN PGP SIGNATURE-----

iQEzBAEBCAAdFiEE...
-----END PGP SIGNATURE-----
`
	payload, err := extractClearSignedPayload([]byte(signed))
	if err != nil {
		t.Fatalf("extractClearSignedPayload: %v", err)
	}

	expected := "Normal line\n- Escaped dash line\n-----ESCAPED MARKER-----\n"
	if string(payload) != expected {
		t.Errorf("got %q, want %q", string(payload), expected)
	}
}
