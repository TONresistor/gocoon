package tl

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestUint32RoundTrip(t *testing.T) {
	cases := []uint32{0, 1, 0x7fffffff, 0x80000000, 0xffffffff}
	for _, v := range cases {
		w := NewWriter()
		w.WriteUint32(v)
		if w.Len() != 4 {
			t.Fatalf("WriteUint32(%d): len=%d, want 4", v, w.Len())
		}
		got, err := NewReader(w.Bytes()).ReadUint32()
		if err != nil {
			t.Fatalf("ReadUint32(%d): %v", v, err)
		}
		if got != v {
			t.Fatalf("ReadUint32: got %#x, want %#x", got, v)
		}
	}
}

func TestUint32WireLittleEndian(t *testing.T) {
	w := NewWriter()
	w.WriteUint32(0x01020304)
	want := []byte{0x04, 0x03, 0x02, 0x01}
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("WriteUint32 wire: got %x, want %x", w.Bytes(), want)
	}
}

func TestInt32Negative(t *testing.T) {
	w := NewWriter()
	w.WriteInt32(-1)
	if !bytes.Equal(w.Bytes(), []byte{0xff, 0xff, 0xff, 0xff}) {
		t.Fatalf("WriteInt32(-1): %x", w.Bytes())
	}
	got, err := NewReader(w.Bytes()).ReadInt32()
	if err != nil || got != -1 {
		t.Fatalf("ReadInt32: %v %d", err, got)
	}
}

func TestUint64RoundTrip(t *testing.T) {
	cases := []uint64{0, 1, math.MaxInt64, math.MaxUint64}
	for _, v := range cases {
		w := NewWriter()
		w.WriteUint64(v)
		got, err := NewReader(w.Bytes()).ReadUint64()
		if err != nil {
			t.Fatalf("ReadUint64(%d): %v", v, err)
		}
		if got != v {
			t.Fatalf("ReadUint64: got %d, want %d", got, v)
		}
	}
}

func TestDoubleRoundTrip(t *testing.T) {
	cases := []float64{0, 1, -1, math.Pi, math.SmallestNonzeroFloat64, math.MaxFloat64, math.NaN()}
	for _, v := range cases {
		w := NewWriter()
		w.WriteDouble(v)
		got, err := NewReader(w.Bytes()).ReadDouble()
		if err != nil {
			t.Fatalf("ReadDouble(%v): %v", v, err)
		}
		if math.IsNaN(v) {
			if !math.IsNaN(got) {
				t.Fatalf("ReadDouble: lost NaN")
			}
			continue
		}
		if got != v {
			t.Fatalf("ReadDouble: got %v, want %v", got, v)
		}
	}
}

func TestInt256RoundTrip(t *testing.T) {
	var v [32]byte
	for i := range v {
		v[i] = byte(i)
	}
	w := NewWriter()
	w.WriteInt256(v)
	if w.Len() != 32 {
		t.Fatalf("WriteInt256: len=%d, want 32", w.Len())
	}
	got, err := NewReader(w.Bytes()).ReadInt256()
	if err != nil {
		t.Fatalf("ReadInt256: %v", err)
	}
	if got != v {
		t.Fatalf("ReadInt256 mismatch")
	}
}

func TestBoolWire(t *testing.T) {
	tests := []struct {
		v    bool
		want []byte
	}{
		{true, []byte{0xb5, 0x75, 0x72, 0x99}},  // 0x997275b5 LE
		{false, []byte{0x37, 0x97, 0x79, 0xbc}}, // 0xbc799737 LE
	}
	for _, tc := range tests {
		w := NewWriter()
		w.WriteBool(tc.v)
		if !bytes.Equal(w.Bytes(), tc.want) {
			t.Errorf("WriteBool(%v): got %x, want %x", tc.v, w.Bytes(), tc.want)
		}
		got, err := NewReader(w.Bytes()).ReadBool()
		if err != nil || got != tc.v {
			t.Errorf("ReadBool: %v %v", err, got)
		}
	}
}

func TestBoolInvalid(t *testing.T) {
	bad := []byte{0xde, 0xad, 0xbe, 0xef}
	_, err := NewReader(bad).ReadBool()
	if !errors.Is(err, ErrInvalidBool) {
		t.Fatalf("ReadBool(bad): err=%v, want ErrInvalidBool", err)
	}
}

// TestBytesShortFormPadding verifies that lengths < 254 use the 1-byte header
// and pad to a 4-byte boundary.
func TestBytesShortFormPadding(t *testing.T) {
	tests := []struct {
		in       []byte
		wantSize int
	}{
		{[]byte{}, 4},                                   // 1 hdr + 0 payload + 3 pad
		{[]byte{0xaa}, 4},                               // 1 + 1 + 2
		{[]byte{0xaa, 0xbb}, 4},                         // 1 + 2 + 1
		{[]byte{0xaa, 0xbb, 0xcc}, 4},                   // 1 + 3 + 0
		{[]byte{0xaa, 0xbb, 0xcc, 0xdd}, 8},             // 1 + 4 + 3
		{[]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, 12}, // 1 + 11 + 0
		{make([]byte, 253), 256},                        // 1 + 253 + 2
	}
	for _, tc := range tests {
		w := NewWriter()
		w.WriteBytes(tc.in)
		if w.Len() != tc.wantSize {
			t.Errorf("WriteBytes(%d bytes): wire size %d, want %d", len(tc.in), w.Len(), tc.wantSize)
			continue
		}
		got, err := NewReader(w.Bytes()).ReadBytes()
		if err != nil {
			t.Errorf("ReadBytes: %v", err)
			continue
		}
		if !bytes.Equal(got, tc.in) {
			t.Errorf("ReadBytes round-trip failed")
		}
	}
}

// TestBytesLongFormPadding verifies the 4-byte header form for lengths >= 254.
func TestBytesLongFormPadding(t *testing.T) {
	tests := []struct {
		size     int
		wantSize int
	}{
		{254, 260},   // 4 hdr + 254 payload + 2 pad
		{255, 260},   // 4 + 255 + 1
		{256, 260},   // 4 + 256 + 0
		{257, 264},   // 4 + 257 + 3
		{1000, 1004}, // 4 + 1000 + 0
	}
	for _, tc := range tests {
		in := make([]byte, tc.size)
		for i := range in {
			in[i] = byte(i)
		}
		w := NewWriter()
		w.WriteBytes(in)
		if w.Len() != tc.wantSize {
			t.Errorf("WriteBytes(%d): wire size %d, want %d", tc.size, w.Len(), tc.wantSize)
			continue
		}
		// Sanity: long form starts with 0xfe.
		if w.Bytes()[0] != 0xfe {
			t.Errorf("WriteBytes(%d): first byte %#x, want 0xfe", tc.size, w.Bytes()[0])
		}
		got, err := NewReader(w.Bytes()).ReadBytes()
		if err != nil {
			t.Errorf("ReadBytes: %v", err)
			continue
		}
		if !bytes.Equal(got, in) {
			t.Errorf("ReadBytes round-trip failed for size %d", tc.size)
		}
	}
}

func TestStringRoundTrip(t *testing.T) {
	tests := []string{"", "hi", "TON", "héllo", strings.Repeat("a", 500)}
	for _, s := range tests {
		w := NewWriter()
		w.WriteString(s)
		got, err := NewReader(w.Bytes()).ReadString()
		if err != nil {
			t.Fatalf("ReadString(%q): %v", s, err)
		}
		if got != s {
			t.Fatalf("ReadString: got %q, want %q", got, s)
		}
	}
}

func TestShortBuffer(t *testing.T) {
	r := NewReader([]byte{1, 2})
	_, err := r.ReadUint32()
	if !errors.Is(err, ErrShortBuffer) {
		t.Fatalf("ReadUint32 short: err=%v", err)
	}
}

func TestInvalidLengthReserved(t *testing.T) {
	r := NewReader([]byte{0xff, 0, 0, 0})
	_, err := r.ReadBytes()
	if !errors.Is(err, ErrInvalidLength) {
		t.Fatalf("ReadBytes(0xff): err=%v", err)
	}
}

func TestReaderPosAdvances(t *testing.T) {
	w := NewWriter()
	w.WriteUint32(42)
	w.WriteString("ab")
	w.WriteBool(true)
	r := NewReader(w.Bytes())
	if _, err := r.ReadUint32(); err != nil {
		t.Fatal(err)
	}
	if r.Pos() != 4 {
		t.Errorf("after uint32: pos=%d, want 4", r.Pos())
	}
	if _, err := r.ReadString(); err != nil {
		t.Fatal(err)
	}
	if r.Pos() != 8 { // 1 hdr + 2 payload + 1 pad = 4
		t.Errorf("after string: pos=%d, want 8", r.Pos())
	}
	if _, err := r.ReadBool(); err != nil {
		t.Fatal(err)
	}
	if !r.EOF() {
		t.Errorf("expected EOF, remaining=%d", r.Remaining())
	}
}

func TestVectorBytesRoundTrip(t *testing.T) {
	in := [][]byte{nil, {1}, {2, 3}, []byte("hello"), make([]byte, 300)}
	w := NewWriter()
	w.WriteVectorBytes(in)
	got, err := NewReader(w.Bytes()).ReadVectorBytes()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in) {
		t.Fatalf("len mismatch: got %d want %d", len(got), len(in))
	}
	for i := range in {
		if !bytes.Equal(got[i], in[i]) {
			t.Errorf("item %d: got %x want %x", i, got[i], in[i])
		}
	}
}

func TestVectorInt256RoundTrip(t *testing.T) {
	in := make([][32]byte, 3)
	for i := range in {
		for j := range in[i] {
			in[i][j] = byte(i*32 + j)
		}
	}
	w := NewWriter()
	w.WriteVectorInt256(in)
	got, err := NewReader(w.Bytes()).ReadVectorInt256()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in) {
		t.Fatalf("len mismatch: %d != %d", len(got), len(in))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("item %d mismatch", i)
		}
	}
}

func TestFlagsAlias(t *testing.T) {
	w := NewWriter()
	w.WriteFlags(0xdeadbeef)
	got, err := NewReader(w.Bytes()).ReadFlags()
	if err != nil {
		t.Fatal(err)
	}
	if got != 0xdeadbeef {
		t.Fatalf("flags round-trip: %#x", got)
	}
}
