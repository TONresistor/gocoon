package router

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"time"
)

// GenerateRATLSClientCert builds a self-signed Ed25519 certificate for the
// client side of a mutual-TLS handshake against a COCOON proxy.
//
// If `nodeKey` is non-nil, the certificate is signed with (and uses the
// public key from) that key. The server identifies a connecting peer by
// the public key embedded in its TLS cert; passing the cocoon-node key
// here makes us recognizable as the on-chain registered client. Pass nil
// to generate a fresh ephemeral keypair (only useful for tests / non-live
// peers that don't validate identity).
//
// Upstream parity: the C++ client builds a similar self-signed cert via
// tee/cocoon/tdx.cpp::generate_tdx_self_signed_cert, embedding a TDX quote
// in custom X.509 OIDs `1.3.6.1.4.1.12345.{1,2}`. In policy=any mode the
// server's verifier does NOT validate OID contents , but it DOES read the
// public key from the cert and treat it as the peer identity. So the key
// matters even if the OIDs are absent.
func GenerateRATLSClientCert(nodeKey ed25519.PrivateKey) (tls.Certificate, error) {
	var pubKey ed25519.PublicKey
	var privKey ed25519.PrivateKey
	if len(nodeKey) == ed25519.PrivateKeySize {
		privKey = nodeKey
		pubKey = nodeKey.Public().(ed25519.PublicKey)
	} else if nodeKey == nil {
		var err error
		pubKey, privKey, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("router: ed25519 keygen: %w", err)
		}
	} else {
		return tls.Certificate{}, fmt.Errorf("router: invalid ed25519 key size %d", len(nodeKey))
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("router: serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "cocoon-client",
			Organization: []string{"TDLib Development"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  false,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
			x509.ExtKeyUsageServerAuth,
		},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pubKey, privKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("router: x509 create: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  privKey,
		Leaf:        template,
	}, nil
}
