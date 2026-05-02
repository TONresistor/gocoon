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
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

// TestDefaultDialerFullStack boots a TCP server that does PoW -> TLS upgrade,
// and verifies our DefaultDialer completes the live upstream handshake.
func TestDefaultDialerFullStack(t *testing.T) {
	const difficulty = 8

	// Self-signed TLS server cert.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	tlsCert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv, Leaf: template}

	// Listen on plain TCP. We do PoW manually then upgrade.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverDone := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

		// Step 1: send PoW challenge.
		var salt [16]byte
		_, _ = rand.Read(salt[:])
		var ch [24]byte
		binary.LittleEndian.PutUint32(ch[0:4], 0x418e1291)
		binary.LittleEndian.PutUint32(ch[4:8], uint32(difficulty))
		copy(ch[8:24], salt[:])
		if _, err := conn.Write(ch[:]); err != nil {
			serverDone <- err
			return
		}

		// Step 2: read PoW response.
		var resp [12]byte
		if _, err := io.ReadFull(conn, resp[:]); err != nil {
			serverDone <- err
			return
		}
		magic := binary.LittleEndian.Uint32(resp[0:4])
		if magic != 0x01827319 {
			serverDone <- errors.New("bad response magic")
			return
		}
		nonce := int64(binary.LittleEndian.Uint64(resp[4:12]))
		if !VerifyPoWResponse(salt, difficulty, nonce) {
			serverDone <- errors.New("pow verification failed")
			return
		}

		// Step 3: TLS upgrade (server-side handshake).
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{tlsCert}})
		if err := tlsConn.HandshakeContext(context.Background()); err != nil {
			serverDone <- err
			return
		}

		_ = tlsConn.Close()
		serverDone <- nil
	}()

	d := &DefaultDialer{HandshakeTimeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := d.DialProxy(ctx, ln.Addr().String())
	if err != nil {
		t.Fatalf("DialProxy: %v", err)
	}
	conn.Close()

	if err := <-serverDone; err != nil {
		t.Fatalf("server side: %v", err)
	}
}
