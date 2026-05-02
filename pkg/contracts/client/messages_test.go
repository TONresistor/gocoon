package client

import (
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/xssnick/tonutils-go/address"
)

func mustAddr(t *testing.T, s string) *address.Address {
	t.Helper()
	a, err := address.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return a
}

const sample = "EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k"

func TestBuildExtTopUp(t *testing.T) {
	excesses := mustAddr(t, sample)
	c, err := BuildExtTopUp(big.NewInt(1_000_000_000), excesses)
	if err != nil {
		t.Fatal(err)
	}
	s := c.BeginParse()
	op, err := s.LoadUInt(32)
	if err != nil {
		t.Fatal(err)
	}
	if op != uint64(OpExtClientTopUp) {
		t.Errorf("op: %#x, want %#x", op, OpExtClientTopUp)
	}
	queryID, _ := s.LoadUInt(64)
	if queryID != 0 {
		t.Errorf("query_id: %d, want 0", queryID)
	}
	amount, _ := s.LoadBigCoins()
	if amount.Cmp(big.NewInt(1_000_000_000)) != 0 {
		t.Errorf("amount mismatch: %s", amount)
	}
	addr, _ := s.LoadAddr()
	if !addrsEqual(addr, excesses) {
		t.Errorf("excesses address mismatch")
	}
	_ = binary.LittleEndian
}

func TestBuildOwnerRequestRefund(t *testing.T) {
	excesses := mustAddr(t, sample)
	c, err := BuildOwnerRequestRefund(excesses)
	if err != nil {
		t.Fatal(err)
	}
	s := c.BeginParse()
	op, _ := s.LoadUInt(32)
	if op != uint64(OpOwnerClientRequestRefund) {
		t.Errorf("op: %#x, want %#x", op, OpOwnerClientRequestRefund)
	}
}

func TestBuildOwnerWithdraw(t *testing.T) {
	excesses := mustAddr(t, sample)
	c, err := BuildOwnerWithdraw(excesses)
	if err != nil {
		t.Fatal(err)
	}
	op, _ := c.BeginParse().LoadUInt(32)
	if op != uint64(OpOwnerClientWithdraw) {
		t.Errorf("op mismatch")
	}
}

func TestBuildOwnerRegister(t *testing.T) {
	excesses := mustAddr(t, sample)
	c, err := BuildOwnerRegister(0xdeadbeef, excesses)
	if err != nil {
		t.Fatal(err)
	}
	s := c.BeginParse()
	op, _ := s.LoadUInt(32)
	if op != uint64(OpOwnerClientRegister) {
		t.Errorf("op: %#x", op)
	}
	_, _ = s.LoadUInt(64)              // query_id
	nonce, _ := s.LoadUInt(64)         // nonce
	if nonce != 0xdeadbeef {
		t.Errorf("nonce: %#x", nonce)
	}
}

func TestBuildOwnerIncreaseStake(t *testing.T) {
	excesses := mustAddr(t, sample)
	c, err := BuildOwnerIncreaseStake(big.NewInt(15_000_000_000), excesses)
	if err != nil {
		t.Fatal(err)
	}
	op, _ := c.BeginParse().LoadUInt(32)
	if op != uint64(OpOwnerClientIncreaseStake) {
		t.Errorf("op: %#x", op)
	}
}

func TestSignedPayloadShape(t *testing.T) {
	expected := mustAddr(t, sample)
	cellp, err := SignedPayload(OpChargeSigned, 42, 1234, expected)
	if err != nil {
		t.Fatal(err)
	}
	s := cellp.BeginParse()
	op, _ := s.LoadUInt(32)
	if op != uint64(OpChargeSigned) {
		t.Errorf("op: %#x", op)
	}
	q, _ := s.LoadUInt(64)
	if q != 42 {
		t.Errorf("queryID: %d", q)
	}
	tokens, _ := s.LoadUInt(64)
	if tokens != 1234 {
		t.Errorf("tokens: %d", tokens)
	}
}

func TestBuildExtChargeSigned(t *testing.T) {
	excesses := mustAddr(t, sample)
	expected := mustAddr(t, sample)
	payload, _ := SignedPayload(OpChargeSigned, 1, 100, expected)
	var sig [64]byte
	for i := range sig {
		sig[i] = byte(i)
	}
	c, err := BuildExtChargeSigned(1, sig, payload, excesses)
	if err != nil {
		t.Fatal(err)
	}
	s := c.BeginParse()
	op, _ := s.LoadUInt(32)
	if op != uint64(OpChargeSigned) {
		t.Errorf("op: %#x", op)
	}
	// query_id + excessesTo + signature(64 bytes) + ref(payload)
	_, _ = s.LoadUInt(64)
	_, _ = s.LoadAddr()
	got, err := s.LoadSlice(64 * 8)
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		if got[i] != byte(i) {
			t.Errorf("signature byte %d: got %#x", i, got[i])
			break
		}
	}
	ref, err := s.LoadRef()
	if err != nil {
		t.Fatal(err)
	}
	pop, _ := ref.LoadUInt(32)
	if pop != uint64(OpChargeSigned) {
		t.Errorf("payload op: %#x", pop)
	}
}

func TestEncodeStorageRoundTrip(t *testing.T) {
	owner := mustAddr(t, sample)
	proxy := mustAddr(t, sample)
	pubKey := big.NewInt(0x1234)
	st := Storage{
		State:        0,
		Balance:      big.NewInt(2_000_000_000),
		Stake:        big.NewInt(1_500_000_000),
		TokensUsed:   42,
		UnlockTs:     0,
		SecretHash:   big.NewInt(0xabcd),
		OwnerAddress: owner,
		ProxyAddress: proxy,
		ProxyPubKey:  pubKey,
	}
	c, err := EncodeStorage(st)
	if err != nil {
		t.Fatal(err)
	}
	s := c.BeginParse()
	gotState, _ := s.LoadUInt(2)
	if gotState != 0 {
		t.Errorf("state mismatch: %d", gotState)
	}
	bal, _ := s.LoadBigCoins()
	if bal.Cmp(st.Balance) != 0 {
		t.Errorf("balance mismatch")
	}
}

func TestRejectsNilExcessesTo(t *testing.T) {
	if _, err := BuildExtTopUp(big.NewInt(100), nil); err == nil {
		t.Errorf("expected error on nil excesses")
	}
	if _, err := BuildOwnerWithdraw(nil); err == nil {
		t.Errorf("expected error on nil excesses")
	}
}

func TestRejectsNegativeAmount(t *testing.T) {
	excesses := mustAddr(t, sample)
	if _, err := BuildExtTopUp(big.NewInt(-1), excesses); err == nil {
		t.Errorf("expected error on negative top up")
	}
}

// addrsEqual compares two TON addresses by their base64 form.
func addrsEqual(a, b *address.Address) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.String() == b.String()
}
