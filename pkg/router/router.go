package router

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"
)

// Dialer abstracts the way we open a stream to an upstream COCOON proxy.
//
// The library default dials TLS directly with PoW pre-handshake. Tests can
// substitute a DialerFunc.
type Dialer interface {
	DialProxy(ctx context.Context, addr string) (net.Conn, error)
}

// DialerFunc adapts a function to the Dialer interface.
type DialerFunc func(ctx context.Context, addr string) (net.Conn, error)

func (f DialerFunc) DialProxy(ctx context.Context, addr string) (net.Conn, error) {
	return f(ctx, addr)
}

// DefaultDialer dials the upstream proxy using the full upstream protocol:
//
//  1. TCP connect
//  2. PoW pre-handshake (read 24-byte challenge, solve, write 12-byte response)
//  3. TLS 1.3 mutual handshake , server REQUIRES a client certificate
//     (SSL_VERIFY_FAIL_IF_NO_PEER_CERT in upstream Tee.cpp:create_ssl_ctx).
//     We present an ephemeral self-signed Ed25519 cert; in policy:any mode
//     the server accepts a plain self-signed cert with no TDX OIDs.
//
// After TLS, control returns immediately to the regular TL framing layer
// (no post-TLS attestation blob is exchanged , the attestation is fully
// embedded in the cert OIDs handled during the TLS handshake itself, per
// the verify_callback in tee/cocoon/RATLS.cpp).
//
// PolicyAny mode: we accept any server cert (InsecureSkipVerify) and the
// server in turn accepts our cert if it has an Ed25519 pubkey (no OIDs
// required when allowed_image_hashes is empty).
type DefaultDialer struct {
	// HandshakeTimeout caps the total time of TCP+PoW+TLS+attestation read.
	// Default 60s.
	HandshakeTimeout time.Duration

	// MaxPoWDifficulty caps the leading-zero-bits the server can ask us to
	// burn. Default MaxDifficulty (32).
	MaxPoWDifficulty int32

	// SkipPoW disables the PoW phase entirely. Set true if connecting to a
	// peer known not to speak PoW. Default false (try PoW; fail if absent).
	SkipPoW bool

	// SkipAttestationRead is retained for compatibility but defaults to
	// true and is ignored by the live path , upstream proxies do not send
	// a post-TLS attestation blob; the attestation is embedded in the TLS
	// cert OIDs handled by the TLS verify callback during the handshake.
	SkipAttestationRead bool

	// SkipAttestationWrite is retained for compatibility but defaults to
	// true. Upstream proxies do not expect a client-side attestation blob
	// post-TLS either.
	SkipAttestationWrite bool

	// AttestationBlob is unused in the live path. Kept for tests that
	// drive a custom dialer flow.
	AttestationBlob []byte

	// ClientKey, when set, is the Ed25519 private key the client cert is
	// built with. Upstream proxies identify the connecting client by the
	// pubkey embedded in this cert (treated as the cocoon-node identity).
	// Passing the cocoon-node wallet key here makes us recognized as the
	// registered client. nil → ephemeral random key (test-only).
	ClientKey ed25519.PrivateKey
}

// DialProxy opens TCP+PoW+TLS+attestation against addr.
func (d *DefaultDialer) DialProxy(ctx context.Context, addr string) (net.Conn, error) {
	if addr == "" {
		return nil, errors.New("router: empty proxy address")
	}
	timeout := d.HandshakeTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	maxPoW := d.MaxPoWDifficulty
	if maxPoW <= 0 {
		maxPoW = MaxDifficulty
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(timeout)
	}

	dialer := &net.Dialer{Timeout: timeout}
	rawConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("router: tcp dial %s: %w", addr, err)
	}
	if err := rawConn.SetDeadline(deadline); err != nil {
		_ = rawConn.Close()
		return nil, err
	}

	// Step 2: PoW pre-handshake (operates on the raw TCP socket).
	if !d.SkipPoW {
		if err := SolvePoW(rawConn, maxPoW); err != nil {
			_ = rawConn.Close()
			return nil, fmt.Errorf("router: pow to %s: %w", addr, err)
		}
	}

	// Step 3: TLS handshake.
	//
	// Mutual TLS: the upstream proxy demands a client certificate. We
	// generate an ephemeral self-signed Ed25519 cert per connection
	// (matches the upstream RA-TLS cert convention without the TDX OIDs
	// since we operate in policy=any mode).
	clientCert, err := GenerateRATLSClientCert(d.ClientKey)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("router: generate client cert: %w", err)
	}
	tlsConn := tls.Client(rawConn, &tls.Config{
		InsecureSkipVerify: true,             //nolint:gosec // policy=any: TEE attestation not enforced via TLS
		MinVersion:         tls.VersionTLS13, // upstream Tee.cpp pins TLS 1.3
		Certificates:       []tls.Certificate{clientCert},
	})
	if err := tlsConn.SetDeadline(deadline); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = tlsConn.Close()
		return nil, fmt.Errorf("router: tls handshake to %s: %w", addr, err)
	}

	// No post-TLS attestation exchange. The attestation lives in cert OIDs
	// (validated during TLS handshake by the verify callback). The next
	// bytes on the wire are tcp.connect from us, then tcp.connected from
	// the peer , handled by pkg/cocoon's transport+handshake layers.
	//
	// The Skip* fields above are now no-ops on the live path. They remain
	// to support custom test dialers that may insert extra steps.
	_ = d.SkipAttestationRead
	_ = d.SkipAttestationWrite

	// Clear the deadline; the caller manages further timeouts.
	_ = tlsConn.SetDeadline(time.Time{})
	return tlsConn, nil
}
