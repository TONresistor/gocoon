// Package bbolt provides a persistent store.Store implementation using
// etcd-io/bbolt. Single-file embedded key-value store, zero external runtime
// dependencies.
//
// Layout (bbolt buckets):
//
//	root             ← single key "snapshot" -> RootSnapshot blob
//	clients          ← key = proxy address  -> ClientSCSnapshot blob
//	payments/<proxy> ← key = request_id     -> SignedPayment blob (one bucket per proxy)
//
// Blobs are encoded with encoding/gob (compact, deterministic, stdlib only).
package bbolt

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"time"

	"github.com/TONresistor/gocoon/pkg/store"
	bolt "go.etcd.io/bbolt"
)

var (
	bucketRoot     = []byte("root")
	bucketClients  = []byte("clients")
	bucketPayments = []byte("payments")
	keyRootSnap    = []byte("snapshot")
)

// Store is a bbolt-backed store.Store.
type Store struct {
	db *bolt.DB
}

// Open opens (or creates) a bbolt database file at path.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("bbolt: open %s: %w", path, err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketRoot, bucketClients, bucketPayments} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bbolt: init buckets: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database file handle.
func (s *Store) Close() error { return s.db.Close() }

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decode(b []byte, v any) error {
	return gob.NewDecoder(bytes.NewReader(b)).Decode(v)
}

// GetRoot returns the cached root snapshot or store.ErrNotFound.
func (s *Store) GetRoot(_ context.Context) (*store.RootSnapshot, error) {
	var snap store.RootSnapshot
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketRoot).Get(keyRootSnap)
		if b == nil {
			return store.ErrNotFound
		}
		return decode(b, &snap)
	})
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

// PutRoot stores the root snapshot.
func (s *Store) PutRoot(_ context.Context, snap *store.RootSnapshot) error {
	if snap == nil {
		return nil
	}
	blob, err := encode(snap)
	if err != nil {
		return fmt.Errorf("bbolt: encode root: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRoot).Put(keyRootSnap, blob)
	})
}

// GetClientSC returns the per-proxy client SC snapshot.
func (s *Store) GetClientSC(_ context.Context, proxy store.ProxyAddress) (*store.ClientSCSnapshot, error) {
	var snap store.ClientSCSnapshot
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketClients).Get([]byte(proxy))
		if b == nil {
			return store.ErrNotFound
		}
		return decode(b, &snap)
	})
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

// PutClientSC stores the per-proxy client SC snapshot.
func (s *Store) PutClientSC(_ context.Context, proxy store.ProxyAddress, snap *store.ClientSCSnapshot) error {
	if snap == nil {
		return nil
	}
	blob, err := encode(snap)
	if err != nil {
		return fmt.Errorf("bbolt: encode client sc: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketClients).Put([]byte(proxy), blob)
	})
}

// DeleteClientSC removes the per-proxy snapshot if present.
func (s *Store) DeleteClientSC(_ context.Context, proxy store.ProxyAddress) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketClients).Delete([]byte(proxy))
	})
}

func paymentBucketName(proxy store.ProxyAddress) []byte {
	out := make([]byte, 0, len(bucketPayments)+1+len(proxy))
	out = append(out, bucketPayments...)
	out = append(out, '/')
	out = append(out, []byte(proxy)...)
	return out
}

// PutSignedPayment stores a signed payment under (proxy, reqID).
func (s *Store) PutSignedPayment(_ context.Context, proxy store.ProxyAddress, reqID store.RequestID, p *store.SignedPayment) error {
	if p == nil {
		return nil
	}
	blob, err := encode(p)
	if err != nil {
		return fmt.Errorf("bbolt: encode payment: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(paymentBucketName(proxy))
		if err != nil {
			return err
		}
		return b.Put(reqID[:], blob)
	})
}

// GetSignedPayment retrieves a signed payment.
func (s *Store) GetSignedPayment(_ context.Context, proxy store.ProxyAddress, reqID store.RequestID) (*store.SignedPayment, error) {
	var p store.SignedPayment
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(paymentBucketName(proxy))
		if b == nil {
			return store.ErrNotFound
		}
		v := b.Get(reqID[:])
		if v == nil {
			return store.ErrNotFound
		}
		return decode(v, &p)
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// DeleteSignedPayment removes a signed payment.
func (s *Store) DeleteSignedPayment(_ context.Context, proxy store.ProxyAddress, reqID store.RequestID) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(paymentBucketName(proxy))
		if b == nil {
			return nil
		}
		return b.Delete(reqID[:])
	})
}

// ListSignedPayments lists all stored payments for a proxy.
func (s *Store) ListSignedPayments(_ context.Context, proxy store.ProxyAddress) ([]store.SignedPaymentEntry, error) {
	var out []store.SignedPaymentEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(paymentBucketName(proxy))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			if len(k) != 32 {
				return errors.New("bbolt: corrupt request id")
			}
			var rid store.RequestID
			copy(rid[:], k)
			var p store.SignedPayment
			if err := decode(v, &p); err != nil {
				return err
			}
			out = append(out, store.SignedPaymentEntry{RequestID: rid, Payment: &p})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GCSignedPayments removes payments older than the threshold.
func (s *Store) GCSignedPayments(_ context.Context, olderThan time.Time) (int, error) {
	removed := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		// Iterate top-level buckets (root, clients, payments, plus per-proxy
		// payment buckets). Per-proxy buckets live at the top level too,
		// keyed by paymentBucketName(proxy).
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			if !bytes.HasPrefix(name, append(bucketPayments, '/')) {
				return nil
			}
			c := b.Cursor()
			var toDelete [][]byte
			for k, v := c.First(); k != nil; k, v = c.Next() {
				var p store.SignedPayment
				if err := decode(v, &p); err != nil {
					continue
				}
				if p.StoredAt.Before(olderThan) {
					toDelete = append(toDelete, append([]byte(nil), k...))
				}
			}
			for _, k := range toDelete {
				if err := b.Delete(k); err != nil {
					return err
				}
				removed++
			}
			return nil
		})
	})
	return removed, err
}

// Compile-time assertion.
var _ store.Store = (*Store)(nil)
