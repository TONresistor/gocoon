// Package store defines the persistence interface used by pkg/cocoon and
// supplies in-memory and bbolt-backed implementations.
//
// All methods take a context.Context. Implementations must be safe for
// concurrent use by multiple goroutines.
package store

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Get* methods when a key has no value.
var ErrNotFound = errors.New("store: not found")

// RootSnapshot caches the last decoded cocoon_root contract state plus the
// masterchain block seqno at which it was read.
type RootSnapshot struct {
	// RawTLO is the TL-encoded rootConfig.Config blob exactly as decoded
	// from the contract storage. Persisted verbatim to allow re-decoding
	// when the schema evolves.
	RawTLO []byte

	// BlockSeqno is the masterchain seqno at which RawTLO was read.
	BlockSeqno uint32

	// FetchedAt is the wall-clock time at which the snapshot was stored.
	FetchedAt time.Time
}

// ClientSCSnapshot captures a get_cocoon_client_data result for a single
// (client, proxy) pair.
type ClientSCSnapshot struct {
	State       uint8     // 0=normal, 1=closing, 2=closed
	Balance     uint64    // nanoTON
	Stake       uint64    // nanoTON
	TokensUsed  uint64
	UnlockTs    uint32    // unix seconds; meaningful when State=closing
	SecretHash  [32]byte
	FetchedAt   time.Time
}

// SignedPayment is a proxy-signed charge or refund-grant blob. Cached so the
// browser/caller can broadcast it on chain at a later time.
type SignedPayment struct {
	Blob       []byte
	StoredAt   time.Time
}

// ProxyAddress is the bounceable string form of a TON address (e.g.
// "EQA..."). We keep it a string in the store interface to stay free of TON
// type imports here.
type ProxyAddress string

// RequestID is a 256-bit identifier for an inference request.
type RequestID [32]byte

// Store is the persistence boundary. Implementations must be concurrency-safe.
type Store interface {
	// Root config caching.
	GetRoot(ctx context.Context) (*RootSnapshot, error)
	PutRoot(ctx context.Context, snap *RootSnapshot) error

	// Per-(client, proxy) channel snapshots.
	GetClientSC(ctx context.Context, proxy ProxyAddress) (*ClientSCSnapshot, error)
	PutClientSC(ctx context.Context, proxy ProxyAddress, snap *ClientSCSnapshot) error
	DeleteClientSC(ctx context.Context, proxy ProxyAddress) error

	// Signed payment ledger. Keys are scoped per proxy.
	PutSignedPayment(ctx context.Context, proxy ProxyAddress, reqID RequestID, p *SignedPayment) error
	GetSignedPayment(ctx context.Context, proxy ProxyAddress, reqID RequestID) (*SignedPayment, error)
	DeleteSignedPayment(ctx context.Context, proxy ProxyAddress, reqID RequestID) error

	// Listing: yields all (reqID, payment) pairs for a proxy.
	ListSignedPayments(ctx context.Context, proxy ProxyAddress) ([]SignedPaymentEntry, error)

	// GC: remove signed payments older than threshold.
	GCSignedPayments(ctx context.Context, olderThan time.Time) (removed int, err error)

	Close() error
}

// SignedPaymentEntry pairs a request ID with its stored payment.
type SignedPaymentEntry struct {
	RequestID RequestID
	Payment   *SignedPayment
}
