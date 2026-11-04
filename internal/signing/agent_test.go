package signing

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/gardenlinux/glbx/internal/signing/pb"
)

func TestServeSignDeterministicAndValid(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not available")
	}
	for _, kt := range []string{"ec", "rsa"} {
		t.Run(kt, func(t *testing.T) {
			dir := t.TempDir()
			keyPath := filepath.Join(dir, "key.pem")
			certPath := filepath.Join(dir, "cert.pem")
			pub, certDER := writeTestKeyAndCert(t, keyPath, certPath, kt)

			backend, err := NewKeyFileBackend(keyPath, certPath)
			if err != nil {
				t.Fatalf("backend: %v", err)
			}

			sock := filepath.Join(dir, "sock")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go Serve(ctx, sock, backend)
			waitForSocket(t, sock)

			msg := []byte("deterministic signing over a unix socket")
			digest := sha256.Sum256(msg)

			sig1 := callSign(t, sock, digest[:])
			sig2 := callSign(t, sock, digest[:])
			if string(sig1) != string(sig2) {
				t.Fatalf("%s: signatures not deterministic", kt)
			}
			if !verify(pub, digest[:], sig1) {
				t.Fatalf("%s: signature does not verify", kt)
			}
			if got := callCert(t, sock); string(got) != string(certDER) {
				t.Fatalf("%s: certificate over socket does not match", kt)
			}
		})
	}
}

func callCert(t *testing.T, sock string) []byte {
	t.Helper()
	conn, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: sock, Net: "unixpacket"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := &pb.Envelope{Msg: &pb.Envelope_CertRequest{CertRequest: &pb.CertRequest{}}}
	data, _ := proto.Marshal(req)
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, maxMsg)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env pb.Envelope
	if err := proto.Unmarshal(buf[:n], &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return env.GetCertResponse().GetDer()
}

func callSign(t *testing.T, sock string, digest []byte) []byte {
	t.Helper()
	conn, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: sock, Net: "unixpacket"})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := &pb.Envelope{Msg: &pb.Envelope_SignRequest{SignRequest: &pb.SignRequest{
		Digest: digest, HashAlgo: "sha256"}}}
	data, _ := proto.Marshal(req)
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, maxMsg)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env pb.Envelope
	if err := proto.Unmarshal(buf[:n], &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	resp := env.GetSignResponse()
	if resp == nil || resp.Error != "" {
		t.Fatalf("sign response error: %q", resp.GetError())
	}
	return resp.Signature
}

func writeTestKeyAndCert(t *testing.T, keyPath, certPath, kt string) (crypto.PublicKey, []byte) {
	t.Helper()
	var signer crypto.Signer
	switch kt {
	case "ec":
		k, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		signer = k
	case "rsa":
		k, _ := rsa.GenerateKey(rand.Reader, 2048)
		signer = k
	}

	der, _ := x509.MarshalPKCS8PrivateKey(signer)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, signer.Public(), signer)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	return signer.Public(), certDER
}

// verify checks a signature as PKCS#11 returns it: raw r||s for EC, raw block for
// RSA PKCS#1 v1.5.
func verify(pub crypto.PublicKey, digest, sig []byte) bool {
	switch pk := pub.(type) {
	case *ecdsa.PublicKey:
		n := len(sig) / 2
		r := new(big.Int).SetBytes(sig[:n])
		s := new(big.Int).SetBytes(sig[n:])
		return ecdsa.Verify(pk, digest, r, s)
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(pk, crypto.SHA256, digest, sig) == nil
	}
	return false
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never appeared", path)
}
