package tl

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
)

// fakeRW pairs a Reader and a Writer to satisfy io.ReadWriter.
type fakeRW struct {
	r io.Reader
	w io.Writer
}

func (f *fakeRW) Read(p []byte) (int, error)  { return f.r.Read(p) }
func (f *fakeRW) Write(p []byte) (int, error) { return f.w.Write(p) }

func TestFramedConnRoundTripPair(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ca := NewFramedConn(a)
	cb := NewFramedConn(b)

	payloads := [][]byte{
		make([]byte, 4),
		[]byte("HELLO_WORLD!"),
		bytes.Repeat([]byte{0xab}, 4096),
	}

	go func() {
		for _, p := range payloads {
			if err := ca.WriteFrame(p); err != nil {
				t.Errorf("WriteFrame: %v", err)
				return
			}
		}
	}()

	for i, want := range payloads {
		got, err := cb.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("frame %d mismatch", i)
		}
	}
}

func TestFramedConnSeqnoMonotonic(t *testing.T) {
	var buf bytes.Buffer
	c := NewFramedConn(&fakeRW{r: &buf, w: &buf})
	for i := 0; i < 5; i++ {
		if err := c.WriteFrame(bytes.Repeat([]byte{0x01}, 4)); err != nil {
			t.Fatal(err)
		}
	}
	if c.OutSeqno() != 5 {
		t.Errorf("out seqno: %d, want 5", c.OutSeqno())
	}
}

func TestFramedConnRejectsBadSeqno(t *testing.T) {
	var buf bytes.Buffer
	// Write a frame manually with seqno=99 (instead of 0).
	frame := []byte{
		0x04, 0x00, 0x00, 0x00, // len=4
		0x63, 0x00, 0x00, 0x00, // seqno=99
		0xde, 0xad, 0xbe, 0xef, // payload
	}
	buf.Write(frame)
	c := NewFramedConn(&fakeRW{r: &buf, w: io.Discard})
	_, err := c.ReadFrame()
	if err == nil || !errors.Is(err, err) || err.Error() == "" {
		t.Fatalf("expected seqno mismatch error, got %v", err)
	}
}

func TestFramedConnLooseMode(t *testing.T) {
	var buf bytes.Buffer
	frame := []byte{
		0x04, 0x00, 0x00, 0x00, // len=4
		0x2a, 0x00, 0x00, 0x00, // seqno=42
		0x01, 0x02, 0x03, 0x04, // payload
	}
	buf.Write(frame)
	c := NewFramedConn(&fakeRW{r: &buf, w: io.Discard})
	c.Strict = false
	got, err := c.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Errorf("payload mismatch")
	}
	if c.InSeqno() != 43 {
		t.Errorf("in seqno: %d, want 43", c.InSeqno())
	}
}

func TestFramedConnRejectsTinyPayload(t *testing.T) {
	var buf bytes.Buffer
	c := NewFramedConn(&fakeRW{r: &buf, w: &buf})
	if err := c.WriteFrame([]byte{0x00, 0x01}); err == nil {
		t.Errorf("expected error for sub-MinFrameSize payload")
	}
}

func TestFramedConnRejectsOversize(t *testing.T) {
	var buf bytes.Buffer
	c := NewFramedConn(&fakeRW{r: &buf, w: &buf})
	too := make([]byte, MaxFrameSize+1)
	if err := c.WriteFrame(too); err == nil {
		t.Errorf("expected error for oversize payload")
	}
}

func TestSimpleFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSimpleFrame(&buf, []byte("attestation blob")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSimpleFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "attestation blob" {
		t.Errorf("got %q", got)
	}
}

func TestSimpleFrameTruncatedHeader(t *testing.T) {
	if _, err := ReadSimpleFrame(bytes.NewReader([]byte{1, 2})); err == nil {
		t.Error("expected error on truncated header")
	}
}
