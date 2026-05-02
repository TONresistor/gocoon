//go:build router_integration

package router

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

// selfSignedTLS spawns a TLS listener with a self-signed cert for testing.
func selfSignedTLS(t *testing.T) (net.Listener, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(1 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	tlsCert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: template}
	cfg := &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ln, ln.Addr().String()
}

func TestDefaultDialerHandshakesNoPoWNoAttestation(t *testing.T) {
	ln, addr := selfSignedTLS(t)
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		if conn == nil {
			return
		}
		// Force the TLS handshake to complete server-side before closing.
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
		_ = conn.Close()
	}()

	d := &DefaultDialer{
		HandshakeTimeout:    5 * time.Second,
		SkipPoW:             true,
		SkipAttestationRead: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d.DialProxy(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}
