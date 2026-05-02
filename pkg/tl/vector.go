package tl

// Vector helpers for the TL `vector t` type.
//
// Wire layout: 4-byte little-endian count followed by count items of type t.
// The type-specific encoding/decoding of each item is delegated to the caller
// via a closure, keeping this package free of generic-codegen concerns.

// ReadVectorLen reads a vector header and returns the element count.
func (r *Reader) ReadVectorLen() (int, error) {
	n, err := r.ReadUint32()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// WriteVectorLen writes a vector header.
func (w *Writer) WriteVectorLen(n int) { w.WriteUint32(uint32(n)) }

// ReadVectorBytes is a convenience wrapper for `vector bytes`.
func (r *Reader) ReadVectorBytes() ([][]byte, error) {
	n, err := r.ReadVectorLen()
	if err != nil {
		return nil, err
	}
	out := make([][]byte, n)
	for i := 0; i < n; i++ {
		b, err := r.ReadBytes()
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

// WriteVectorBytes is a convenience wrapper for `vector bytes`.
func (w *Writer) WriteVectorBytes(items [][]byte) {
	w.WriteVectorLen(len(items))
	for _, b := range items {
		w.WriteBytes(b)
	}
}

// ReadVectorString is a convenience wrapper for `vector string`.
func (r *Reader) ReadVectorString() ([]string, error) {
	n, err := r.ReadVectorLen()
	if err != nil {
		return nil, err
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		s, err := r.ReadString()
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

// WriteVectorString is a convenience wrapper for `vector string`.
func (w *Writer) WriteVectorString(items []string) {
	w.WriteVectorLen(len(items))
	for _, s := range items {
		w.WriteString(s)
	}
}

// ReadVectorInt256 is a convenience wrapper for `vector int256` (used for
// proxy_hashes, worker_hashes, etc. in rootConfig).
func (r *Reader) ReadVectorInt256() ([][32]byte, error) {
	n, err := r.ReadVectorLen()
	if err != nil {
		return nil, err
	}
	out := make([][32]byte, n)
	for i := 0; i < n; i++ {
		v, err := r.ReadInt256()
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// WriteVectorInt256 is a convenience wrapper for `vector int256`.
func (w *Writer) WriteVectorInt256(items [][32]byte) {
	w.WriteVectorLen(len(items))
	for _, v := range items {
		w.WriteInt256(v)
	}
}
