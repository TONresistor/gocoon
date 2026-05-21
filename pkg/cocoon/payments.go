package cocoon

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/TONresistor/gocoon/pkg/contracts/client"
	"github.com/TONresistor/gocoon/pkg/store"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// PreparedMessage is a build-time output: the destination address and the
// signed external-message body, ready to be broadcast.
type PreparedMessage struct {
	Destination *address.Address
	Body        *cell.Cell
}

// ChannelOps provides the four payment-channel operations a client can
// initiate against a deployed cocoon_client SC.
type ChannelOps struct {
	clientSCAddr *address.Address
	excessesTo   *address.Address
}

// NewChannelOps returns a ChannelOps for the given (client SC, excesses recipient).
func NewChannelOps(clientSCAddr, excessesTo *address.Address) (*ChannelOps, error) {
	if clientSCAddr == nil {
		return nil, errors.New("cocoon: NewChannelOps: clientSCAddr nil")
	}
	if excessesTo == nil {
		return nil, errors.New("cocoon: NewChannelOps: excessesTo nil")
	}
	return &ChannelOps{clientSCAddr: clientSCAddr, excessesTo: excessesTo}, nil
}

// PrepareTopUp builds an ext_client_top_up external message body.
func (o *ChannelOps) PrepareTopUp(amount *big.Int) (*PreparedMessage, error) {
	body, err := client.BuildExtTopUp(amount, o.excessesTo)
	if err != nil {
		return nil, err
	}
	return &PreparedMessage{Destination: o.clientSCAddr, Body: body}, nil
}

// PrepareWithdraw builds an owner_client_withdraw body.
func (o *ChannelOps) PrepareWithdraw() (*PreparedMessage, error) {
	body, err := client.BuildOwnerWithdraw(o.excessesTo)
	if err != nil {
		return nil, err
	}
	return &PreparedMessage{Destination: o.clientSCAddr, Body: body}, nil
}

// PrepareRequestRefund builds an owner_client_request_refund body.
func (o *ChannelOps) PrepareRequestRefund() (*PreparedMessage, error) {
	body, err := client.BuildOwnerRequestRefund(o.excessesTo)
	if err != nil {
		return nil, err
	}
	return &PreparedMessage{Destination: o.clientSCAddr, Body: body}, nil
}

// PrepareIncreaseStake builds an owner_client_increase_stake body.
func (o *ChannelOps) PrepareIncreaseStake(amount *big.Int) (*PreparedMessage, error) {
	body, err := client.BuildOwnerIncreaseStake(amount, o.excessesTo)
	if err != nil {
		return nil, err
	}
	return &PreparedMessage{Destination: o.clientSCAddr, Body: body}, nil
}

// PaymentLedger caches signed payments observed from the proxy.
//
// The proxy may issue many signed payments per session; the client keeps the
// latest one per request_id and may decide to batch-commit them on chain
// later (matching upstream behavior of avoiding ~0.01 TON gas per ack).
type PaymentLedger struct {
	store store.Store
}

// NewPaymentLedger returns a ledger backed by the given store.
func NewPaymentLedger(s store.Store) *PaymentLedger {
	return &PaymentLedger{store: s}
}

// Record stores the latest signed payment for (proxy, request).
func (l *PaymentLedger) Record(ctx context.Context, proxy store.ProxyAddress, reqID store.RequestID, blob []byte) error {
	if l.store == nil {
		return errors.New("cocoon: PaymentLedger has no store")
	}
	return l.store.PutSignedPayment(ctx, proxy, reqID, &store.SignedPayment{
		Blob:     blob,
		StoredAt: time.Now().UTC(),
	})
}

// List returns every payment stored for the proxy.
func (l *PaymentLedger) List(ctx context.Context, proxy store.ProxyAddress) ([]store.SignedPaymentEntry, error) {
	if l.store == nil {
		return nil, errors.New("cocoon: PaymentLedger has no store")
	}
	return l.store.ListSignedPayments(ctx, proxy)
}

// GC removes payments older than the threshold.
func (l *PaymentLedger) GC(ctx context.Context, olderThan time.Time) (int, error) {
	if l.store == nil {
		return 0, errors.New("cocoon: PaymentLedger has no store")
	}
	return l.store.GCSignedPayments(ctx, olderThan)
}

// Send is a convenience that prepares + broadcasts.
//
// op is one of "topup", "withdraw", "close", "stake".
func (s *Session) Send(ctx context.Context, op string, broadcaster Broadcaster, args ...*big.Int) (string, error) {
	if broadcaster == nil {
		return "", errors.New("cocoon: nil broadcaster")
	}
	ops, err := s.ChannelOps()
	if err != nil {
		return "", err
	}
	var prepared *PreparedMessage
	switch op {
	case "topup":
		if len(args) != 1 || args[0] == nil {
			return "", fmt.Errorf("cocoon: Send(topup): need amount")
		}
		prepared, err = ops.PrepareTopUp(args[0])
	case "withdraw":
		prepared, err = ops.PrepareWithdraw()
	case "close":
		prepared, err = ops.PrepareRequestRefund()
	case "stake":
		if len(args) != 1 || args[0] == nil {
			return "", fmt.Errorf("cocoon: Send(stake): need amount")
		}
		prepared, err = ops.PrepareIncreaseStake(args[0])
	default:
		return "", fmt.Errorf("cocoon: Send: unknown op %q", op)
	}
	if err != nil {
		return "", err
	}
	return broadcaster.BroadcastExternal(ctx, prepared.Destination, prepared.Body)
}

// (Session.ChannelOps is implemented in session.go now that handshake captures
// the client SC address.)
