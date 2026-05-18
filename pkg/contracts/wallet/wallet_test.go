package wallet

import (
	"crypto/ed25519"
	"testing"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

const sample = "EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k"

func mustAddr(t *testing.T) *address.Address {
	t.Helper()
	a, err := address.ParseAddr(sample)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestEncodeStorageRejectsInvalid(t *testing.T) {
	if _, err := EncodeStorage(Config{PublicKey: nil, OwnerAddress: mustAddr(t)}); err == nil {
		t.Errorf("expected error on nil pubkey")
	}
	if _, err := EncodeStorage(Config{PublicKey: make(ed25519.PublicKey, 16), OwnerAddress: mustAddr(t)}); err == nil {
		t.Errorf("expected error on wrong-length pubkey")
	}
	pk, _, _ := ed25519.GenerateKey(nil)
	if _, err := EncodeStorage(Config{PublicKey: pk, OwnerAddress: nil}); err == nil {
		t.Errorf("expected error on nil owner")
	}
}

func TestEncodeStorageRoundTrip(t *testing.T) {
	pk, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := EncodeStorage(Config{PublicKey: pk, OwnerAddress: mustAddr(t)})
	if err != nil {
		t.Fatal(err)
	}
	s := c.MustBeginParse()
	seqno, _ := s.LoadInt(32)
	if seqno != 0 {
		t.Errorf("seqno should start at 0, got %d", seqno)
	}
	subwallet, _ := s.LoadInt(32)
	if subwallet != 0 {
		t.Errorf("subwallet should start at 0")
	}
	gotPK, _ := s.LoadSlice(256)
	if string(gotPK) != string(pk) {
		t.Errorf("pubkey mismatch")
	}
	status, _ := s.LoadUInt(32)
	if status != 0 {
		t.Errorf("status should be 0")
	}
}

func TestBuildOutboundMessageCellShape(t *testing.T) {
	to := mustAddr(t)
	c, err := BuildOutboundMessageCell(OutboundMessage{
		To:     to,
		Value:  1_000_000_000,
		Body:   nil,
		Bounce: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := c.MustBeginParse()
	flag, _ := s.LoadUInt(6)
	if flag != 0x18 {
		t.Errorf("bounce flag: %#x, want 0x18", flag)
	}
}

func TestCreateSignedExternalMessage(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub
	to := mustAddr(t)
	body := cell.BeginCell().MustStoreUInt(0xdeadbeef, 32).EndCell()
	msgs := []OutboundMessage{{
		To:     to,
		Value:  100_000_000,
		Body:   body,
		Bounce: true,
	}}
	out, err := CreateSignedExternalMessage(msgs, priv, SignedExternalMessageOpts{Seqno: 5, ValidUntil: 1_700_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Fatal("nil cell")
	}
	// Parse: 64-byte signature, then subwalletId/validUntil/seqno, then 8-bit mode + ref msg.
	s := out.MustBeginParse()
	sig, err := s.LoadSlice(64 * 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != 64 {
		t.Errorf("signature length: %d", len(sig))
	}
	subwallet, _ := s.LoadUInt(32)
	if subwallet != 0 {
		t.Errorf("subwallet: %d", subwallet)
	}
	validUntil, _ := s.LoadUInt(32)
	if validUntil != 1_700_000_000 {
		t.Errorf("validUntil: %d", validUntil)
	}
	seqno, _ := s.LoadUInt(32)
	if seqno != 5 {
		t.Errorf("seqno: %d", seqno)
	}
	mode, _ := s.LoadUInt(8)
	if mode != 1 {
		t.Errorf("default mode: %d, want 1", mode)
	}
}

func TestDeriveAddressDeterministic(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	cfg := Config{PublicKey: pub, OwnerAddress: mustAddr(t)}
	// Use a minimal stand-in code cell. The exact address depends on the real
	// cocoon_wallet code; this test only ensures determinism, not parity.
	code := cell.BeginCell().MustStoreUInt(0x1234, 16).EndCell()
	a1, err := DeriveAddress(cfg, code)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := DeriveAddress(cfg, code)
	if err != nil {
		t.Fatal(err)
	}
	if a1.String() != a2.String() {
		t.Errorf("non-deterministic: %s vs %s", a1, a2)
	}
}

func TestCodeFromBocBytesHex(t *testing.T) {
	// Use a tiny but valid BoC: an empty cell.
	empty := cell.BeginCell().EndCell()
	bocBytes := empty.ToBOC()
	hexStr := ""
	for _, b := range bocBytes {
		const digits = "0123456789abcdef"
		hexStr += string(digits[b>>4]) + string(digits[b&0xf])
	}
	json := `{"hex":"` + hexStr + `"}`
	c, err := CodeFromBocBytes([]byte(json))
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("nil cell")
	}
}
