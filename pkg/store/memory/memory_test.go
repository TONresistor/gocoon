package memory

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/TONresistor/gocoon/pkg/store"
)

func TestRootRoundTrip(t *testing.T) {
	s := New()
	ctx := context.Background()

	if _, err := s.GetRoot(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetRoot empty: err=%v, want ErrNotFound", err)
	}

	snap := &store.RootSnapshot{
		RawTLO:     []byte{1, 2, 3, 4},
		BlockSeqno: 42,
		FetchedAt:  time.Now(),
	}
	if err := s.PutRoot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.RawTLO, snap.RawTLO) {
		t.Errorf("raw mismatch")
	}
	if got.BlockSeqno != snap.BlockSeqno {
		t.Errorf("seqno mismatch")
	}

	// Mutating the returned copy must not affect storage.
	got.RawTLO[0] = 0xff
	again, _ := s.GetRoot(ctx)
	if again.RawTLO[0] == 0xff {
		t.Errorf("Get returned mutable reference")
	}
}

func TestClientSCRoundTrip(t *testing.T) {
	s := New()
	ctx := context.Background()
	const proxy = store.ProxyAddress("EQTest")

	if _, err := s.GetClientSC(ctx, proxy); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expected ErrNotFound")
	}

	snap := &store.ClientSCSnapshot{
		State:      0,
		Balance:    1_000_000_000,
		Stake:      500_000_000,
		TokensUsed: 12345,
		UnlockTs:   0,
	}
	if err := s.PutClientSC(ctx, proxy, snap); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetClientSC(ctx, proxy)
	if err != nil {
		t.Fatal(err)
	}
	if got.Balance != snap.Balance || got.Stake != snap.Stake || got.TokensUsed != snap.TokensUsed {
		t.Errorf("mismatch: %+v vs %+v", got, snap)
	}

	if err := s.DeleteClientSC(ctx, proxy); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetClientSC(ctx, proxy); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expected ErrNotFound after delete")
	}
}

func TestSignedPaymentLedger(t *testing.T) {
	s := New()
	ctx := context.Background()
	const proxy = store.ProxyAddress("EQTest")
	var rid store.RequestID
	rid[0] = 0xab

	if _, err := s.GetSignedPayment(ctx, proxy, rid); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expected ErrNotFound")
	}

	now := time.Now()
	p := &store.SignedPayment{Blob: []byte("payload"), StoredAt: now}
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

	if err := s.DeleteSignedPayment(ctx, proxy, rid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSignedPayment(ctx, proxy, rid); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expected ErrNotFound after delete")
	}
}

func TestSignedPaymentGC(t *testing.T) {
	s := New()
	ctx := context.Background()
	const proxy = store.ProxyAddress("EQTest")

	now := time.Now()
	old := &store.SignedPayment{Blob: []byte("old"), StoredAt: now.Add(-2 * time.Hour)}
	fresh := &store.SignedPayment{Blob: []byte("fresh"), StoredAt: now}

	var oldID, freshID store.RequestID
	oldID[0] = 1
	freshID[0] = 2

	_ = s.PutSignedPayment(ctx, proxy, oldID, old)
	_ = s.PutSignedPayment(ctx, proxy, freshID, fresh)

	removed, err := s.GCSignedPayments(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed=%d, want 1", removed)
	}
	if _, err := s.GetSignedPayment(ctx, proxy, oldID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("old not removed")
	}
	if _, err := s.GetSignedPayment(ctx, proxy, freshID); err != nil {
		t.Errorf("fresh removed: %v", err)
	}
}

// TestConcurrent stresses the store with many goroutines to expose data races
// (run with go test -race).
func TestConcurrent(t *testing.T) {
	s := New()
	ctx := context.Background()
	var wg sync.WaitGroup
	const goroutines = 50
	const ops = 100

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			proxy := store.ProxyAddress("EQ" + string(rune('A'+id%26)))
			for j := 0; j < ops; j++ {
				var rid store.RequestID
				rid[0] = byte(j)
				_ = s.PutSignedPayment(ctx, proxy, rid, &store.SignedPayment{
					Blob:     []byte{byte(id), byte(j)},
					StoredAt: time.Now(),
				})
				_, _ = s.GetSignedPayment(ctx, proxy, rid)
				_, _ = s.ListSignedPayments(ctx, proxy)
			}
		}(i)
	}
	wg.Wait()
}
