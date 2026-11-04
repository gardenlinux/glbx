// Package signing runs the host side of glbx's out-of-container signing: a small
// server that answers "give me the public key" and "sign this digest" requests
// over a Unix socket, so a build container can obtain signatures without ever
// holding the private key.
//
// The private key lives only here, on the host. Inside the container a PKCS#11
// module forwards every signing operation to this server over the socket; the
// container sees signatures, never key bytes. The Backend that actually produces
// signatures is pluggable: this file implements a key-file backend (shelling out
// to openssl), and a future backend could instead drive a hardware token, a
// cloud KMS, or a secrets manager without any change to the module or the
// container.
package signing

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"

	pb "github.com/gardenlinux/glbx/internal/signing/pb"
)

// Backend produces signatures and exposes the matching public key. It is key-type
// agnostic to its callers: the backend decides EC vs RSA from the key it holds.
type Backend interface {
	// PublicKey returns the public parameters a PKCS#11 consumer needs to
	// describe the key object (never any private material).
	PublicKey() *pb.PublicKeyResponse
	// Sign returns a signature over the given pre-computed digest. For EC keys
	// the result is raw r||s; for RSA it is the raw PKCS#1 v1.5 block.
	Sign(digest []byte, hashAlgo string) ([]byte, error)
	// Certificate returns the public X.509 certificate in DER form.
	Certificate() []byte
}

// keyFileBackend signs with an on-disk PEM private key by invoking the openssl
// CLI. EC signing uses a deterministic (RFC 6979) nonce so a given digest always
// yields the same signature.
type keyFileBackend struct {
	path    string
	isEC    bool
	coordSz int // EC: byte width of r and s (the curve field size)
	pub     *pb.PublicKeyResponse
	certDER []byte
}

// NewKeyFileBackend loads a PEM private key and its matching PEM certificate and
// prepares a backend for them.
func NewKeyFileBackend(keyPath, certPath string) (Backend, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("signing: no PEM block in %s", keyPath)
	}
	priv, err := parsePrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	certDER, err := readCertDER(certPath)
	if err != nil {
		return nil, err
	}

	b := &keyFileBackend{path: keyPath, certDER: certDER}
	switch pk := priv.(type) {
	case *ecdsa.PrivateKey:
		b.isEC = true
		b.coordSz = (pk.Curve.Params().BitSize + 7) / 8
		params, point, err := ecPublic(pk)
		if err != nil {
			return nil, err
		}
		b.pub = &pb.PublicKeyResponse{Key: &pb.PublicKeyResponse_Ec{
			Ec: &pb.ECPublicKey{Params: params, Point: point}}}
	case *rsa.PrivateKey:
		b.pub = &pb.PublicKeyResponse{Key: &pb.PublicKeyResponse_Rsa{
			Rsa: &pb.RSAPublicKey{Modulus: pk.N.Bytes(), Exponent: bigEndian(pk.E)}}}
	default:
		return nil, fmt.Errorf("signing: unsupported key type %T", pk)
	}
	return b, nil
}

func (b *keyFileBackend) PublicKey() *pb.PublicKeyResponse { return b.pub }

func (b *keyFileBackend) Certificate() []byte { return b.certDER }

func (b *keyFileBackend) Sign(digest []byte, hashAlgo string) ([]byte, error) {
	tmp, err := os.CreateTemp("", "glbx-digest-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(digest); err != nil {
		return nil, err
	}
	tmp.Close()

	args := []string{"pkeyutl", "-sign", "-inkey", b.path, "-in", tmp.Name(),
		"-pkeyopt", "digest:" + hashAlgo}
	if b.isEC {
		args = append(args, "-pkeyopt", "nonce-type:1")
	}

	cmd := exec.Command("openssl", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("signing: openssl: %v: %s", err, errb.String())
	}

	sig := out.Bytes()
	if b.isEC {
		// PKCS#11 CKM_ECDSA expects raw r||s; openssl emits a DER SEQUENCE.
		return derToRawECDSA(sig, b.coordSz)
	}
	return sig, nil
}

// readCertDER reads a PEM certificate file and returns its DER bytes.
func readCertDER(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("signing: no PEM block in %s", path)
	}
	return block.Bytes, nil
}

func parsePrivateKey(der []byte) (any, error) {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	return x509.ParsePKCS1PrivateKey(der)
}

func ecPublic(pk *ecdsa.PrivateKey) (params, point []byte, err error) {
	oid, ok := curveOID(pk.Curve)
	if !ok {
		return nil, nil, fmt.Errorf("signing: unsupported curve %s", pk.Curve.Params().Name)
	}
	params, err = asn1.Marshal(oid)
	if err != nil {
		return nil, nil, err
	}
	raw := elliptic.Marshal(pk.Curve, pk.X, pk.Y)
	point, err = asn1.Marshal(raw) // DER OCTET STRING wrapping the point
	return params, point, err
}

func curveOID(c elliptic.Curve) (asn1.ObjectIdentifier, bool) {
	switch c {
	case elliptic.P256():
		return asn1.ObjectIdentifier{1, 2, 840, 10045, 3, 1, 7}, true
	case elliptic.P384():
		return asn1.ObjectIdentifier{1, 3, 132, 0, 34}, true
	case elliptic.P521():
		return asn1.ObjectIdentifier{1, 3, 132, 0, 35}, true
	}
	return nil, false
}

func derToRawECDSA(der []byte, coordSz int) ([]byte, error) {
	var s struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &s); err != nil {
		return nil, fmt.Errorf("signing: parse ecdsa der: %w", err)
	}
	out := make([]byte, 2*coordSz)
	s.R.FillBytes(out[:coordSz])
	s.S.FillBytes(out[coordSz:])
	return out, nil
}

func bigEndian(e int) []byte {
	b := []byte{byte(e >> 24), byte(e >> 16), byte(e >> 8), byte(e)}
	for len(b) > 1 && b[0] == 0 {
		b = b[1:]
	}
	return b
}
