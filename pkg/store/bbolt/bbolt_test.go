package bbolt

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/TONresistor/gocoon/pkg/store"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRootRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.GetRoot(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expected ErrNotFound")
	}

	snap := &store.RootSnapshot{
		RawTLO:     []byte{1, 2, 3, 4},
		BlockSeqno: 42,
		FetchedAt:  time.Now().UTC().Truncate(time.Second),
	}
	if err := s.PutRoot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.RawTLO, snap.RawTLO) || got.BlockSeqno != snap.BlockSeqno {
		t.Errorf("mismatch: %+v vs %+v", got, snap)
	}
}

func TestPersistsAcrossOpens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.bolt")
	ctx := context.Background()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutRoot(ctx, &store.RootSnapshot{RawTLO: []byte("hello"), BlockSeqno: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.GetRoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.RawTLO, []byte("hello")) {
		t.Errorf("survived restart: %v", got.RawTLO)
	}
}

func TestSignedPaymentLedger(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const proxy = store.ProxyAddress("EQTest")
	var rid store.RequestID
	rid[0] = 0xab

	if _, err := s.GetSignedPayment(ctx, proxy, rid); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expected ErrNotFound")
	}

	p := &store.SignedPayment{Blob: []byte("payload"), StoredAt: time.Now().UTC()}
	if err := s.PutSignedPayment(ctx, proxy, rid, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSignedPayment(ctx, proxy, rid)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Blob, p.Blob) {
		t.Errorf("blob mismatch")
	}

	list, err := s.ListSignedPayments(ctx, proxy)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].RequestID != rid {
		t.Errorf("list: %+v", list)
	}
}

func TestSignedPaymentGC(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const proxy = store.ProxyAddress("EQGC")
	now := time.Now().UTC()

	for i, st := range []time.Time{now.Add(-2 * time.Hour), now} {
		var rid store.RequestID
		rid[0] = byte(i + 1)
		if err := s.PutSignedPayment(ctx, proxy, rid, &store.SignedPayment{
			Blob:     []byte{byte(i)},
			StoredAt: st,
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := s.GCSignedPayments(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed=%d, want 1", removed)
	}
}

func TestClientSCRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const proxy = store.ProxyAddress("EQCli")

	snap := &store.ClientSCSnapshot{State: 1, Balance: 1e9, Stake: 5e8, TokensUsed: 42}
	if err := s.PutClientSC(ctx, proxy, snap); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetClientSC(ctx, proxy)
	if err != nil {
		t.Fatal(err)
	}
	if got.Balance != snap.Balance || got.State != snap.State {
		t.Errorf("mismatch")
	}
	if err := s.DeleteClientSC(ctx, proxy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetClientSC(ctx, proxy); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete")
	}
}
