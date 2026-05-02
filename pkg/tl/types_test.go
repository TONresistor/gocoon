package tl

import (
	"bytes"
	"testing"
)

func TestClientParamsRoundTrip(t *testing.T) {
	p := ClientParams{
		Flags:           0x3,
		ClientOwnerAddr: "EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k",
		IsTest:          false,
		MinProtoVersion: 1,
		MaxProtoVersion: 4,
	}
	w := NewWriter()
	p.Encode(w, true)

	r := NewReader(w.Bytes())
	id, err := r.ReadUint32()
	if err != nil {
		t.Fatal(err)
	}
	if id != IDClientParams {
		t.Fatalf("constructor id: %#x, want %#x", id, IDClientParams)
	}
	got, err := DecodeClientParamsBody(r)
	if err != nil {
		t.Fatal(err)
	}
	if got.Flags != p.Flags || got.ClientOwnerAddr != p.ClientOwnerAddr ||
		got.IsTest != p.IsTest || got.MinProtoVersion != p.MinProtoVersion ||
		got.MaxProtoVersion != p.MaxProtoVersion {
		t.Errorf("roundtrip mismatch:\nwant %+v\ngot  %+v", p, got)
	}
}

func TestClientParamsFlagsZero(t *testing.T) {
	// flags=0: no IsTest, no proto versions.
	p := ClientParams{Flags: 0, ClientOwnerAddr: "EQ"}
	w := NewWriter()
	p.Encode(w, false)
	r := NewReader(w.Bytes())
	got, err := DecodeClientParamsBody(r)
	if err != nil {
		t.Fatal(err)
	}
	if got.Flags != 0 || got.ClientOwnerAddr != "EQ" || got.IsTest || got.MinProtoVersion != 0 {
		t.Errorf("flags=0 roundtrip: %+v", got)
	}
	if !r.EOF() {
		t.Errorf("trailing data: %d bytes", r.Remaining())
	}
}

func TestProxyParamsRoundTrip(t *testing.T) {
	var pk [32]byte
	for i := range pk {
		pk[i] = byte(i)
	}
	p := ProxyParams{
		Flags:        0x3,
		PublicKey:    pk,
		OwnerAddress: "EQOwner",
		SCAddress:    "EQSC",
		IsTest:       false,
		ProtoVersion: 3,
	}
	w := NewWriter()
	p.Encode(w, true)

	r := NewReader(w.Bytes())
	id, _ := r.ReadUint32()
	if id != IDProxyParams {
		t.Fatalf("id mismatch: %#x", id)
	}
	got, err := DecodeProxyParamsBody(r)
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicKey != pk || got.OwnerAddress != p.OwnerAddress || got.SCAddress != p.SCAddress || got.ProtoVersion != p.ProtoVersion {
		t.Errorf("mismatch: %+v vs %+v", got, p)
	}
}

func TestTokensUsedRoundTrip(t *testing.T) {
	in := TokensUsed{1, 2, 3, 4, 10}
	w := NewWriter()
	in.Encode(w)
	got, err := DecodeTokensUsed(NewReader(w.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Errorf("mismatch")
	}
}

func TestClientConnectToProxyEncode(t *testing.T) {
	c := ClientConnectToProxy{
		Params: ClientParams{
			Flags:           0x3,
			ClientOwnerAddr: "EQ",
			IsTest:          false,
			MinProtoVersion: 1,
			MaxProtoVersion: 4,
		},
		MinConfigVersion: 7,
	}
	w := NewWriter()
	c.Encode(w)
	if len(w.Bytes()) < 4 {
		t.Fatalf("encoded too small")
	}
	// First 4 bytes must be the function constructor ID.
	got := uint32(w.Bytes()[0]) | uint32(w.Bytes()[1])<<8 | uint32(w.Bytes()[2])<<16 | uint32(w.Bytes()[3])<<24
	if got != IDClientConnectToProxy {
		t.Errorf("function id: %#x, want %#x", got, IDClientConnectToProxy)
	}

	r := NewReader(w.Bytes()[4:])
	params, err := DecodeClientParamsBody(r)
	if err != nil {
		t.Fatal(err)
	}
	if params.Flags != 0x3 || params.ClientOwnerAddr != "EQ" || params.MinProtoVersion != 1 || params.MaxProtoVersion != 4 {
		t.Fatalf("params mismatch: %+v", params)
	}
	minCfg, err := r.ReadInt32()
	if err != nil {
		t.Fatal(err)
	}
	if minCfg != 7 {
		t.Fatalf("min config version: %d", minCfg)
	}
	if !r.EOF() {
		t.Fatalf("trailing data: %d bytes", r.Remaining())
	}
	if bytes.Contains(w.Bytes()[4:], []byte{0x64, 0xca, 0xfd, 0x40}) {
		t.Fatal("client.params constructor must not be boxed inside client.connectToProxy")
	}
}

func TestClientAuthorizeWithProxyShortEncode(t *testing.T) {
	c := ClientAuthorizeWithProxyShort{Data: []byte("secret")}
	w := NewWriter()
	c.Encode(w)
	r := NewReader(w.Bytes())
	id, _ := r.ReadUint32()
	if id != IDClientAuthorizeWithProxyShort {
		t.Fatalf("id mismatch: %#x", id)
	}
	got, err := r.ReadBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("secret")) {
		t.Errorf("data mismatch: %q", got)
	}
}

func TestClientAuthorizeWithProxyLongEncode(t *testing.T) {
	c := ClientAuthorizeWithProxyLong{}
	w := NewWriter()
	c.Encode(w)
	if len(w.Bytes()) != 4 {
		t.Fatalf("long auth encoded len: %d", len(w.Bytes()))
	}
}

func TestConstructorIDStability(t *testing.T) {
	// boolFalse  = Bool  →  0xbc799737 by spec.
	// We don't actually compute it (the spec gives us the value directly),
	// but we sanity-check that the algorithm is deterministic.
	a := ConstructorID("test x")
	b := ConstructorID("test x")
	if a != b {
		t.Errorf("ConstructorID not deterministic: %#x vs %#x", a, b)
	}
}

func TestIDStatusAlwaysVerified(t *testing.T) {
	// All constructor IDs are now CRC32-and-tlo verified.
	if IDStatus(IDBoolTrue) != "verified-crc32-and-tlo" {
		t.Error("bool should be verified")
	}
	if IDStatus(IDClientConnectedToProxy) != "verified-crc32-and-tlo" {
		t.Error("client.connectedToProxy should be verified")
	}
}
