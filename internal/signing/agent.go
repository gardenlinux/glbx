// Package signing runs the host side of glbx's out-of-container signing: a small
// server that answers "give me the public key", "give me the certificate", and
// "sign this digest" requests over a Unix socket, so a build container can obtain
// signatures without ever holding the private key.
//
// The private key lives only here, on the host. Inside the container a PKCS#11
// module forwards every signing operation to this server over the socket; the
// container sees signatures, never key bytes. The Backend that actually produces
// signatures is pluggable: this file implements a key-file backend (signing with
// Go's standard library), and a future backend could instead drive a hardware
// token, a cloud KMS, or a secrets manager without any change to the module or
// the container.
package signing

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"

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

// keyFileBackend signs with an on-disk PEM private key using the Go standard
// library. EC signing derives the nonce per RFC 6979, so a given digest always
// yields the same signature (Go's crypto/ecdsa is randomized and has no
// deterministic mode); RSA PKCS#1 v1.5 is deterministic by construction.
type keyFileBackend struct {
	ec      *ecdsa.PrivateKey // set when the key is EC
	rsa     *rsa.PrivateKey   // set when the key is RSA
	coordSz int               // EC: byte width of r and s (the curve field size)
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

	b := &keyFileBackend{certDER: certDER}
	switch pk := priv.(type) {
	case *ecdsa.PrivateKey:
		b.ec = pk
		b.coordSz = (pk.Curve.Params().BitSize + 7) / 8
		params, point, err := ecPublic(pk)
		if err != nil {
			return nil, err
		}
		b.pub = &pb.PublicKeyResponse{Key: &pb.PublicKeyResponse_Ec{
			Ec: &pb.ECPublicKey{Params: params, Point: point}}}
	case *rsa.PrivateKey:
		b.rsa = pk
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
	if hashAlgo != "sha256" {
		return nil, fmt.Errorf("signing: unsupported hash %q", hashAlgo)
	}
	if b.ec != nil {
		// PKCS#11 CKM_ECDSA expects raw r||s; produce it directly from the
		// deterministically-signed (r, s).
		r, s := signECDSADeterministic(b.ec, digest)
		out := make([]byte, 2*b.coordSz)
		r.FillBytes(out[:b.coordSz])
		s.FillBytes(out[b.coordSz:])
		return out, nil
	}
	return rsa.SignPKCS1v15(nil, b.rsa, crypto.SHA256, digest)
}

// signECDSADeterministic signs digest with an RFC 6979 nonce. Because glbx owns
// both the signer here and the kernel's verifier, the signature only needs to be
// a deterministic function of (key, digest); RFC 6979 provides that and is also
// interoperable with other RFC 6979 implementations.
func signECDSADeterministic(priv *ecdsa.PrivateKey, digest []byte) (r, s *big.Int) {
	c := priv.Curve.Params()
	n := c.N
	k := rfc6979Nonce(priv, digest)
	for {
		rx, _ := c.ScalarBaseMult(k.Bytes())
		r = new(big.Int).Mod(rx, n)
		if r.Sign() == 0 {
			k = k.Add(k, big.NewInt(1)) // degenerate; try the next k
			continue
		}
		e := hashToInt(digest, n)
		kInv := new(big.Int).ModInverse(k, n)
		s = new(big.Int).Mul(priv.D, r)
		s.Add(s, e)
		s.Mul(s, kInv)
		s.Mod(s, n)
		if s.Sign() != 0 {
			return r, s
		}
		k = k.Add(k, big.NewInt(1))
	}
}

// rfc6979Nonce derives the deterministic ECDSA nonce k for a SHA-256 digest,
// following RFC 6979 §3.2 with HMAC-SHA256.
func rfc6979Nonce(priv *ecdsa.PrivateKey, digest []byte) *big.Int {
	n := priv.Curve.Params().N
	qlen := n.BitLen()
	rlen := (qlen + 7) / 8
	hlen := sha256.Size

	int2octets := func(x *big.Int) []byte {
		b := x.Bytes()
		if len(b) < rlen {
			p := make([]byte, rlen)
			copy(p[rlen-len(b):], b)
			return p
		}
		return b[len(b)-rlen:]
	}
	e := hashToInt(digest, n)
	bits2octets := int2octets(e)

	mac := func(key, data []byte) []byte {
		m := hmac.New(sha256.New, key)
		m.Write(data)
		return m.Sum(nil)
	}

	v := make([]byte, hlen)
	for i := range v {
		v[i] = 0x01
	}
	k := make([]byte, hlen)
	privOct := int2octets(priv.D)
	k = mac(k, concat(v, []byte{0x00}, privOct, bits2octets))
	v = mac(k, v)
	k = mac(k, concat(v, []byte{0x01}, privOct, bits2octets))
	v = mac(k, v)
	for {
		var t []byte
		for len(t) < rlen {
			v = mac(k, v)
			t = append(t, v...)
		}
		kk := hashToInt(t, n)
		if kk.Sign() > 0 && kk.Cmp(n) < 0 {
			return kk
		}
		k = mac(k, append(v, 0x00))
		v = mac(k, v)
	}
}

// hashToInt converts a hash/HMAC output to an integer per RFC 6979 / FIPS 186-4:
// take the leftmost N bits, where N is the bit length of the curve order.
func hashToInt(b []byte, n *big.Int) *big.Int {
	v := new(big.Int).SetBytes(b)
	if excess := len(b)*8 - n.BitLen(); excess > 0 {
		v.Rsh(v, uint(excess))
	}
	return v
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
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

func bigEndian(e int) []byte {
	b := []byte{byte(e >> 24), byte(e >> 16), byte(e >> 8), byte(e)}
	for len(b) > 1 && b[0] == 0 {
		b = b[1:]
	}
	return b
}
