package cocoon

import (
	"context"
	"errors"
	"sync"
	"time"
)

// QueryID is the 64-bit identifier for an in-flight TL query.
// Matches upstream TcpClient::QueryId (td::int64).
type QueryID int64

// pendingQuery is one outstanding request waiting for its answer.
type pendingQuery struct {
	answer   chan []byte
	err      chan error
	deadline time.Time
}

// correlator multiplexes concurrent queries over one TL session.
//
// Each query carries a unique QueryID; the proxy's response (tcp.queryAnswer
// or tcp.queryError) echoes the same ID. The correlator dispatches incoming
// frames to the matching pending channel, with a per-query timeout.
type correlator struct {
	mu      sync.Mutex
	pending map[QueryID]*pendingQuery
	closed  bool
}

func newCorrelator() *correlator {
	return &correlator{pending: make(map[QueryID]*pendingQuery)}
}

// register adds a pending query and returns the channels to receive on.
func (c *correlator) register(id QueryID, timeout time.Duration) (<-chan []byte, <-chan error, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, errors.New("correlator: closed")
	}
	if _, dup := c.pending[id]; dup {
		return nil, nil, errors.New("correlator: duplicate query id")
	}
	pq := &pendingQuery{
		answer:   make(chan []byte, 1),
		err:      make(chan error, 1),
		deadline: time.Now().Add(timeout),
	}
	c.pending[id] = pq
	return pq.answer, pq.err, nil
}

// deliver routes an answer to the matching pending query.
func (c *correlator) deliver(id QueryID, payload []byte) bool {
	c.mu.Lock()
	pq, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case pq.answer <- payload:
	default:
	}
	return true
}

// fail routes an error to the matching pending query.
func (c *correlator) fail(id QueryID, err error) bool {
	c.mu.Lock()
	pq, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case pq.err <- err:
	default:
	}
	return true
}

// failAll resolves every pending query with err. Used during shutdown.
func (c *correlator) failAll(err error) {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[QueryID]*pendingQuery)
	c.closed = true
	c.mu.Unlock()
	for _, pq := range pending {
		select {
		case pq.err <- err:
		default:
		}
	}
}

// gcExpired drops entries past their deadline. Returns the count.
func (c *correlator) gcExpired(now time.Time) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0
	}
	count := 0
	for id, pq := range c.pending {
		if now.After(pq.deadline) {
			delete(c.pending, id)
			select {
			case pq.err <- ErrRequestTimeout:
			default:
			}
			count++
		}
	}
	return count
}

// runGC starts a background goroutine that periodically reclaims expired
// queries until ctx is done or the correlator is closed.
func (c *correlator) runGC(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c.gcExpired(now)
		}
	}
}
