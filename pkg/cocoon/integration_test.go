package cocoon

import (
	"context"
	"crypto/ed25519"
	"net"
	"testing"
	"time"

	"github.com/TONresistor/gocoon/pkg/cocoon/internal/fakeproxy"
	"github.com/TONresistor/gocoon/pkg/router"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

func parseAddr(t *testing.T, s string) *address.Address {
	t.Helper()
	a, err := address.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestSessionConnectAgainstFakeProxy boots a fakeproxy on localhost (plain TCP,
// no TLS), connects via a custom Dialer, and verifies the handshake completes
// through to SessionReady with the correct client SC address parsed from the
// proxy's reply.
func TestSessionConnectAgainstFakeProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv, addr, err := fakeproxy.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	// Plain TCP dialer for the fake (no TLS, no PoW, no attestation).
	d := router.DialerFunc(func(_ context.Context, _ string) (net.Conn, error) {
		return net.Dial("tcp", addr)
	})
	_ = router.DefaultDialer{} // ensure default dialer compiles

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	owner := parseAddr(t, "EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k")
	cli, err := New(ctx, Config{
		OwnerAddress:        owner,
		NodeKey:             priv,
		RootAddress:         owner,
		CocoonWalletAddress: owner,
		Router:              d,
		Broadcaster:         &noopBroadcaster{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	sess, err := cli.ConnectProxy(ctx, addr)
	if err != nil {
		t.Fatalf("ConnectProxy: %v", err)
	}
	if sess.Status() != SessionReady {
		t.Fatalf("session status: %v", sess.Status())
	}
	if sess.ClientSCAddress() == "" {
		t.Errorf("expected non-empty client SC address")
	}

	// Payment status should round-trip.
	ps, err := sess.PaymentStatus(ctx)
	if err != nil {
		t.Fatalf("PaymentStatus: %v", err)
	}
	if ps.TokensMax != 1_000_000 {
		t.Errorf("max tokens: %d", ps.TokensMax)
	}
}

// noopBroadcaster satisfies cocoon.Broadcaster but never actually broadcasts.
type noopBroadcaster struct{}

func (n *noopBroadcaster) BroadcastExternal(ctx context.Context, dest *address.Address, body *cell.Cell) (string, error) {
	return "deadbeef", nil
}
