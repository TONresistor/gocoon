package tl

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync/atomic"
)

// MaxFrameSize is the inclusive maximum payload size (16 MiB). Frames larger
// than this are rejected to avoid memory exhaustion.
const MaxFrameSize = 16 * 1024 * 1024

// MinFrameSize is the smallest valid payload (4 bytes = constructor ID).
const MinFrameSize = 4

// Wire frame format used after TLS+attestation, derived from upstream
// net/TcpConnection.cpp. This is the FINAL framing for application traffic:
//
//	[uint32 LE: payload_len] [int32 LE: seqno] [payload bytes]
//
// payload_len counts only the payload (NOT seqno or len fields).
// seqno is per-direction monotonic int32, starting at 0, increment per packet.
// No CRC: integrity is provided by TLS.
// Total wire bytes = 4 + 4 + payload_len.

// FramedConn wraps an io.ReadWriter to provide framed read/write with
// independent in/out seqno counters, matching upstream net/TcpConnection.cpp.
//
// Concurrency: WriteFrame and ReadFrame may be called concurrently from
// different goroutines (one writer, one reader), but each side must be
// serialized.
type FramedConn struct {
	rw       io.ReadWriter
	outSeqno atomic.Int32
	inSeqno  atomic.Int32
	// Strict, when true, rejects frames whose seqno does not match the
	// expected counter. Matches upstream behavior.
	Strict bool
}

// NewFramedConn returns a FramedConn around rw, with seqno counters at 0 and
// strict seqno validation enabled.
func NewFramedConn(rw io.ReadWriter) *FramedConn {
	return &FramedConn{rw: rw, Strict: true}
}

// WriteFrame writes one framed payload, prepending the next outgoing seqno.
func (c *FramedConn) WriteFrame(payload []byte) error {
	n := len(payload)
	if n < MinFrameSize {
		return fmt.Errorf("frame: payload %d too small (min %d for boxed TL)", n, MinFrameSize)
	}
	if uint64(n) > MaxFrameSize {
		return fmt.Errorf("frame: payload %d exceeds MaxFrameSize %d", n, MaxFrameSize)
	}
	seqno := c.outSeqno.Add(1) - 1 // 0-based
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(n))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(seqno))
	if _, err := c.rw.Write(hdr[:]); err != nil {
		return fmt.Errorf("frame: write header: %w", err)
	}
	if _, err := c.rw.Write(payload); err != nil {
		return fmt.Errorf("frame: write payload: %w", err)
	}
	return nil
}

// ReadFrame reads the next framed payload, validating the incoming seqno.
func (c *FramedConn) ReadFrame() ([]byte, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(c.rw, hdr[:]); err != nil {
		return nil, fmt.Errorf("frame: read header: %w", err)
	}
	plen := binary.LittleEndian.Uint32(hdr[0:4])
	seqno := int32(binary.LittleEndian.Uint32(hdr[4:8]))
	if plen < MinFrameSize {
		return nil, fmt.Errorf("frame: payload_len %d below MinFrameSize", plen)
	}
	if plen > MaxFrameSize {
		return nil, fmt.Errorf("frame: payload_len %d exceeds MaxFrameSize", plen)
	}
	if c.Strict {
		expected := c.inSeqno.Add(1) - 1
		if seqno != expected {
			return nil, fmt.Errorf("frame: seqno mismatch: got %d, want %d", seqno, expected)
		}
	} else {
		c.inSeqno.Store(seqno + 1)
	}
	payload := make([]byte, plen)
	if _, err := io.ReadFull(c.rw, payload); err != nil {
		return nil, fmt.Errorf("frame: read payload: %w", err)
	}
	return payload, nil
}

// OutSeqno returns the next seqno that will be assigned to an outgoing frame.
func (c *FramedConn) OutSeqno() int32 { return c.outSeqno.Load() }

// InSeqno returns the next expected seqno on the receive side.
func (c *FramedConn) InSeqno() int32 { return c.inSeqno.Load() }

// ReadSimpleFrame reads a [uint32 LE: len][bytes] frame without seqno.
// This format is used during the RA-TLS attestation handshake, before the
// regular framing loop kicks in.
//
// Source: tdnet/td/net/FramedPipe.cpp framed_read.
func ReadSimpleFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("simple-frame: read len: %w", err)
	}
	plen := binary.LittleEndian.Uint32(hdr[:])
	if plen > MaxFrameSize {
		return nil, fmt.Errorf("simple-frame: len %d exceeds MaxFrameSize", plen)
	}
	payload := make([]byte, plen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("simple-frame: read payload: %w", err)
	}
	return payload, nil
}

// WriteSimpleFrame writes a [uint32 LE: len][bytes] frame.
func WriteSimpleFrame(w io.Writer, payload []byte) error {
	if uint64(len(payload)) > MaxFrameSize {
		return fmt.Errorf("simple-frame: payload exceeds MaxFrameSize")
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("simple-frame: write len: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("simple-frame: write payload: %w", err)
	}
	return nil
}
