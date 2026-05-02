package cocoon

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/TONresistor/gocoon/pkg/store"
	memstore "github.com/TONresistor/gocoon/pkg/store/memory"
	"github.com/xssnick/tonutils-go/address"
)

func sampleAddr(t *testing.T) *address.Address {
	t.Helper()
	a, err := address.ParseAddr("EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestChannelOpsRequiresAddrs(t *testing.T) {
	if _, err := NewChannelOps(nil, sampleAddr(t)); err == nil {
		t.Errorf("expected error on nil clientSCAddr")
	}
	if _, err := NewChannelOps(sampleAddr(t), nil); err == nil {
		t.Errorf("expected error on nil excessesTo")
	}
}

func TestChannelOpsPrepare(t *testing.T) {
	ops, err := NewChannelOps(sampleAddr(t), sampleAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	if p, err := ops.PrepareTopUp(big.NewInt(1_000_000_000)); err != nil || p == nil {
		t.Errorf("PrepareTopUp: %v %v", p, err)
	}
	if p, err := ops.PrepareWithdraw(); err != nil || p == nil {
		t.Errorf("PrepareWithdraw: %v %v", p, err)
	}
	if p, err := ops.PrepareRequestRefund(); err != nil || p == nil {
		t.Errorf("PrepareRequestRefund: %v %v", p, err)
	}
	if p, err := ops.PrepareIncreaseStake(big.NewInt(5_000_000_000)); err != nil || p == nil {
		t.Errorf("PrepareIncreaseStake: %v %v", p, err)
	}
}

func TestPaymentLedgerRoundTrip(t *testing.T) {
	l := NewPaymentLedger(memstore.New())
	ctx := context.Background()
	proxy := store.ProxyAddress("EQTest")
	var rid store.RequestID
	rid[0] = 0x42

	if err := l.Record(ctx, proxy, rid, []byte("blob")); err != nil {
		t.Fatal(err)
	}
	list, err := l.List(ctx, proxy)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].RequestID != rid {
		t.Errorf("list: %+v", list)
	}
}

func TestPaymentLedgerGC(t *testing.T) {
	s := memstore.New()
	l := NewPaymentLedger(s)
	ctx := context.Background()
	proxy := store.ProxyAddress("EQTest")

	now := time.Now()
	old := store.RequestID{1}
	fresh := store.RequestID{2}
	_ = s.PutSignedPayment(ctx, proxy, old, &store.SignedPayment{Blob: []byte("o"), StoredAt: now.Add(-2 * time.Hour)})
	_ = s.PutSignedPayment(ctx, proxy, fresh, &store.SignedPayment{Blob: []byte("f"), StoredAt: now})

	removed, err := l.GC(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed=%d, want 1", removed)
	}
}
