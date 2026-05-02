package router

import (
	"fmt"
	"io"

	"github.com/TONresistor/gocoon/pkg/tl"
)

// ReadAttestation reads one simple-framed attestation blob from r and returns
// it. The caller may discard the bytes (PolicyAny mode) or pass them to a TDX
// quote validator (PolicyStrict mode, future v2).
//
// Wire format (matches upstream tdnet/td/net/FramedPipe.cpp::framed_read):
//
//	[uint32 LE: payload_len][payload bytes]
//
// This frame is exchanged once after the TLS handshake completes and BEFORE
// the regular [len][seqno][payload] traffic begins. Source:
// TcpConnection.cpp:248-255 framed_tl_read call.
//
// Maximum 16 MiB to avoid memory exhaustion on a hostile peer.
func ReadAttestation(r io.Reader) ([]byte, error) {
	blob, err := tl.ReadSimpleFrame(r)
	if err != nil {
		return nil, fmt.Errorf("router: read attestation: %w", err)
	}
	return blob, nil
}

// WriteAttestation writes a simple-framed attestation blob. Used by the
// server side or by tests.
func WriteAttestation(w io.Writer, blob []byte) error {
	return tl.WriteSimpleFrame(w, blob)
}
