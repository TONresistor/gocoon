// Package memory provides an in-memory implementation of store.Store.
//
// Useful for tests and for the standalone CLI when persistence is not desired.
// Concurrency-safe.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/TONresistor/gocoon/pkg/store"
)

// Store is an in-memory store.Store implementation.
type Store struct {
	mu       sync.RWMutex
	root     *store.RootSnapshot
	clients  map[store.ProxyAddress]*store.ClientSCSnapshot
	payments map[store.ProxyAddress]map[store.RequestID]*store.SignedPayment
}

// New returns an empty Store.
func New() *Store {
	return &Store{
		clients:  make(map[store.ProxyAddress]*store.ClientSCSnapshot),
		payments: make(map[store.ProxyAddress]map[store.RequestID]*store.SignedPayment),
	}
}

// GetRoot returns the cached root snapshot or store.ErrNotFound.
func (s *Store) GetRoot(_ context.Context) (*store.RootSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.root == nil {
		return nil, store.ErrNotFound
	}
	cp := *s.root
	cp.RawTLO = append([]byte(nil), s.root.RawTLO...)
	return &cp, nil
}

// PutRoot stores a root snapshot.
func (s *Store) PutRoot(_ context.Context, snap *store.RootSnapshot) error {
	if snap == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *snap
	cp.RawTLO = append([]byte(nil), snap.RawTLO...)
	s.root = &cp
	return nil
}

// GetClientSC returns the per-proxy client SC snapshot.
func (s *Store) GetClientSC(_ context.Context, proxy store.ProxyAddress) (*store.ClientSCSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.clients[proxy]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

// PutClientSC stores the per-proxy client SC snapshot.
func (s *Store) PutClientSC(_ context.Context, proxy store.ProxyAddress, snap *store.ClientSCSnapshot) error {
	if snap == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *snap
	s.clients[proxy] = &cp
	return nil
}

// DeleteClientSC removes the per-proxy snapshot if present.
func (s *Store) DeleteClientSC(_ context.Context, proxy store.ProxyAddress) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, proxy)
	return nil
}

// PutSignedPayment stores a signed payment under (proxy, reqID).
func (s *Store) PutSignedPayment(_ context.Context, proxy store.ProxyAddress, reqID store.RequestID, p *store.SignedPayment) error {
	if p == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket, ok := s.payments[proxy]
	if !ok {
		bucket = make(map[store.RequestID]*store.SignedPayment)
		s.payments[proxy] = bucket
	}
	cp := *p
	cp.Blob = append([]byte(nil), p.Blob...)
	bucket[reqID] = &cp
	return nil
}

// GetSignedPayment retrieves a signed payment.
func (s *Store) GetSignedPayment(_ context.Context, proxy store.ProxyAddress, reqID store.RequestID) (*store.SignedPayment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bucket, ok := s.payments[proxy]
	if !ok {
		return nil, store.ErrNotFound
	}
	p, ok := bucket[reqID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *p
	cp.Blob = append([]byte(nil), p.Blob...)
	return &cp, nil
}

// DeleteSignedPayment removes a signed payment if present.
func (s *Store) DeleteSignedPayment(_ context.Context, proxy store.ProxyAddress, reqID store.RequestID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	bucket, ok := s.payments[proxy]
	if !ok {
		return nil
	}
	delete(bucket, reqID)
	if len(bucket) == 0 {
		delete(s.payments, proxy)
	}
	return nil
}

// ListSignedPayments lists all stored payments for a proxy.
func (s *Store) ListSignedPayments(_ context.Context, proxy store.ProxyAddress) ([]store.SignedPaymentEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bucket, ok := s.payments[proxy]
	if !ok {
		return nil, nil
	}
	out := make([]store.SignedPaymentEntry, 0, len(bucket))
	for k, v := range bucket {
		cp := *v
		cp.Blob = append([]byte(nil), v.Blob...)
		out = append(out, store.SignedPaymentEntry{RequestID: k, Payment: &cp})
	}
	return out, nil
}

// GCSignedPayments removes all payments stored before the threshold.
func (s *Store) GCSignedPayments(_ context.Context, olderThan time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for proxy, bucket := range s.payments {
		for k, v := range bucket {
			if v.StoredAt.Before(olderThan) {
				delete(bucket, k)
				removed++
			}
		}
		if len(bucket) == 0 {
			delete(s.payments, proxy)
		}
	}
	return removed, nil
}

// Close is a no-op for the in-memory store.
func (s *Store) Close() error { return nil }

// Compile-time assertion that *Store implements store.Store.
var _ store.Store = (*Store)(nil)
