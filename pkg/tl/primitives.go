package tl

import (
	"encoding/binary"
	"fmt"
)

// Bool constructor IDs per the standard TL schema.
const (
	boolFalseID uint32 = 0xbc799737
	boolTrueID  uint32 = 0x997275b5
)

// Reader decodes TL primitives from a byte slice.
//
// The Reader is not safe for concurrent use. All read methods advance the
// internal cursor; on error the cursor is left at the position of the first
// failed read so the caller can inspect what was already decoded.
type Reader struct {
	buf []byte
	pos int
}

// NewReader returns a Reader that reads from buf without copying.
func NewReader(buf []byte) *Reader {
	return &Reader{buf: buf}
}

// Pos returns the current read offset.
func (r *Reader) Pos() int { return r.pos }

// Remaining returns the number of unread bytes.
func (r *Reader) Remaining() int { return len(r.buf) - r.pos }

// EOF reports whether the cursor is at the end of the buffer.
func (r *Reader) EOF() bool { return r.pos >= len(r.buf) }

func (r *Reader) need(n int) error {
	if r.pos+n > len(r.buf) {
		return fmt.Errorf("%w: need %d at offset %d (buffer size %d)", ErrShortBuffer, n, r.pos, len(r.buf))
	}
	return nil
}

// ReadInt32 decodes a 32-bit little-endian signed integer.
func (r *Reader) ReadInt32() (int32, error) {
	v, err := r.ReadUint32()
	return int32(v), err
}

// ReadUint32 decodes a 32-bit little-endian unsigned integer.
func (r *Reader) ReadUint32() (uint32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint32(r.buf[r.pos:])
	r.pos += 4
	return v, nil
}

// ReadInt64 decodes a 64-bit little-endian signed integer.
func (r *Reader) ReadInt64() (int64, error) {
	v, err := r.ReadUint64()
	return int64(v), err
}

// ReadUint64 decodes a 64-bit little-endian unsigned integer.
func (r *Reader) ReadUint64() (uint64, error) {
	if err := r.need(8); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint64(r.buf[r.pos:])
	r.pos += 8
	return v, nil
}

// ReadDouble decodes an IEEE-754 double precision float in little-endian.
func (r *Reader) ReadDouble() (float64, error) {
	if err := r.need(8); err != nil {
		return 0, err
	}
	bits := binary.LittleEndian.Uint64(r.buf[r.pos:])
	r.pos += 8
	return float64FromBits(bits), nil
}

// ReadInt128 reads 16 raw bytes.
func (r *Reader) ReadInt128() ([16]byte, error) {
	var out [16]byte
	if err := r.need(16); err != nil {
		return out, err
	}
	copy(out[:], r.buf[r.pos:r.pos+16])
	r.pos += 16
	return out, nil
}

// ReadInt256 reads 32 raw bytes.
func (r *Reader) ReadInt256() ([32]byte, error) {
	var out [32]byte
	if err := r.need(32); err != nil {
		return out, err
	}
	copy(out[:], r.buf[r.pos:r.pos+32])
	r.pos += 32
	return out, nil
}

// ReadBool decodes a TL bool constructor.
func (r *Reader) ReadBool() (bool, error) {
	id, err := r.ReadUint32()
	if err != nil {
		return false, err
	}
	switch id {
	case boolTrueID:
		return true, nil
	case boolFalseID:
		return false, nil
	default:
		return false, fmt.Errorf("%w: 0x%08x", ErrInvalidBool, id)
	}
}

// ReadBytes decodes a length-prefixed byte slice with 4-byte alignment.
//
// The returned slice is a copy of the underlying buffer.
func (r *Reader) ReadBytes() ([]byte, error) {
	length, headerLen, err := r.readLengthPrefix()
	if err != nil {
		return nil, err
	}
	if err := r.need(length); err != nil {
		return nil, err
	}
	out := make([]byte, length)
	copy(out, r.buf[r.pos:r.pos+length])
	r.pos += length
	// Padding so the total (header + payload) is aligned to 4.
	total := headerLen + length
	pad := (4 - total%4) % 4
	if pad > 0 {
		if err := r.need(pad); err != nil {
			return nil, err
		}
		r.pos += pad
	}
	return out, nil
}

// ReadString decodes a TL string (same wire format as bytes, UTF-8 expected
// but not validated here).
func (r *Reader) ReadString() (string, error) {
	b, err := r.ReadBytes()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// readLengthPrefix returns the payload length and the number of header bytes
// consumed (1 or 4). 0xff is reserved.
func (r *Reader) readLengthPrefix() (length, headerLen int, err error) {
	if err := r.need(1); err != nil {
		return 0, 0, err
	}
	first := r.buf[r.pos]
	if first == 0xff {
		return 0, 0, fmt.Errorf("%w: leading byte 0xff is reserved", ErrInvalidLength)
	}
	if first < 0xfe {
		r.pos++
		return int(first), 1, nil
	}
	// first == 0xfe: 3-byte length follows.
	if err := r.need(4); err != nil {
		return 0, 0, err
	}
	length = int(r.buf[r.pos+1]) | int(r.buf[r.pos+2])<<8 | int(r.buf[r.pos+3])<<16
	r.pos += 4
	return length, 4, nil
}

// ReadFlags decodes a flags:# field (a 4-byte bitmask).
func (r *Reader) ReadFlags() (uint32, error) {
	return r.ReadUint32()
}

// ReadRaw copies n bytes from the buffer and advances the cursor.
func (r *Reader) ReadRaw(n int) ([]byte, error) {
	if err := r.need(n); err != nil {
		return nil, err
	}
	out := make([]byte, n)
	copy(out, r.buf[r.pos:r.pos+n])
	r.pos += n
	return out, nil
}

// Writer encodes TL primitives.
//
// Writer is not safe for concurrent use. Bytes returned by Bytes() are owned
// by the Writer; copy them if you need to retain them past the next mutation.
type Writer struct {
	buf []byte
}

// NewWriter returns an empty Writer.
func NewWriter() *Writer { return &Writer{} }

// NewWriterCap returns a Writer pre-allocated to capacity n.
func NewWriterCap(n int) *Writer { return &Writer{buf: make([]byte, 0, n)} }

// Bytes returns the encoded bytes. The caller must copy if retention is needed.
func (w *Writer) Bytes() []byte { return w.buf }

// Len returns the current encoded byte count.
func (w *Writer) Len() int { return len(w.buf) }

// Reset clears the buffer (keeping capacity).
func (w *Writer) Reset() { w.buf = w.buf[:0] }

// WriteInt32 appends a 32-bit signed integer.
func (w *Writer) WriteInt32(v int32) { w.WriteUint32(uint32(v)) }

// WriteUint32 appends a 32-bit unsigned integer.
func (w *Writer) WriteUint32(v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	w.buf = append(w.buf, b[:]...)
}

// WriteInt64 appends a 64-bit signed integer.
func (w *Writer) WriteInt64(v int64) { w.WriteUint64(uint64(v)) }

// WriteUint64 appends a 64-bit unsigned integer.
func (w *Writer) WriteUint64(v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	w.buf = append(w.buf, b[:]...)
}

// WriteDouble appends an IEEE-754 double precision float.
func (w *Writer) WriteDouble(v float64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], float64ToBits(v))
	w.buf = append(w.buf, b[:]...)
}

// WriteInt128 appends 16 raw bytes.
func (w *Writer) WriteInt128(v [16]byte) { w.buf = append(w.buf, v[:]...) }

// WriteInt256 appends 32 raw bytes.
func (w *Writer) WriteInt256(v [32]byte) { w.buf = append(w.buf, v[:]...) }

// WriteBool appends a bool constructor.
func (w *Writer) WriteBool(v bool) {
	if v {
		w.WriteUint32(boolTrueID)
	} else {
		w.WriteUint32(boolFalseID)
	}
}

// WriteBytes appends a length-prefixed byte slice with 4-byte alignment padding.
func (w *Writer) WriteBytes(b []byte) {
	length := len(b)
	if length < 0xfe {
		w.buf = append(w.buf, byte(length))
		w.buf = append(w.buf, b...)
		// Pad to 4 with zero bytes.
		total := 1 + length
		pad := (4 - total%4) % 4
		for i := 0; i < pad; i++ {
			w.buf = append(w.buf, 0)
		}
		return
	}
	// Long form: 0xfe, 3-byte LE length.
	w.buf = append(w.buf, 0xfe, byte(length), byte(length>>8), byte(length>>16))
	w.buf = append(w.buf, b...)
	total := 4 + length
	pad := (4 - total%4) % 4
	for i := 0; i < pad; i++ {
		w.buf = append(w.buf, 0)
	}
}

// WriteString appends a TL string (length-prefixed UTF-8 bytes).
func (w *Writer) WriteString(s string) { w.WriteBytes([]byte(s)) }

// WriteFlags appends a flags:# field.
func (w *Writer) WriteFlags(flags uint32) { w.WriteUint32(flags) }

// WriteRaw appends raw bytes without length prefix or padding.
func (w *Writer) WriteRaw(b []byte) { w.buf = append(w.buf, b...) }
