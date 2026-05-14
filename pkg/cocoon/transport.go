package cocoon

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TONresistor/gocoon/pkg/tl"
)

// transport carries the framed TL session over a single TLS connection.
// It runs one read goroutine that dispatches incoming tcp.* packets to:
//   - the correlator (for tcp.queryAnswer / tcp.queryError)
//   - a streaming handler (for tcp.packet, used by streaming inference parts)
//
// All writes go through SendQuery / SendPacket which serialize via writeMu.
type transport struct {
	conn   net.Conn
	frames *tl.FramedConn
	corr   *correlator
	logger *slog.Logger

	writeMu sync.Mutex

	// inited gates the post-tcp.connect application traffic. Until then,
	// only tcp.connect/tcp.connected are exchanged.
	inited atomic.Bool

	// onPacket receives tcp.packet payloads (fire-and-forget messages from
	// the proxy, e.g. async progress updates).
	onPacket func(payload []byte)

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  atomic.Pointer[error]
}

func newTransport(conn net.Conn, corr *correlator, logger *slog.Logger) *transport {
	return &transport{
		conn:   conn,
		frames: tl.NewFramedConn(conn),
		corr:   corr,
		logger: logger,
		closed: make(chan struct{}),
	}
}

// Run starts the read loop. Returns when the connection closes or ctx is done.
// Must be called once per transport. Errors are surfaced via Closed().
func (t *transport) Run(ctx context.Context) {
	defer t.closeWithErr(io.EOF)
	for {
		select {
		case <-ctx.Done():
			t.closeWithErr(ctx.Err())
			return
		case <-t.closed:
			return
		default:
		}
		payload, err := t.frames.ReadFrame()
		if err != nil {
			t.closeWithErr(fmt.Errorf("transport: read: %w", err))
			return
		}
		if err := t.dispatch(payload); err != nil {
			t.logger.Warn("transport: dispatch error", "err", err)
		}
	}
}

// dispatch parses one frame payload and routes it.
func (t *transport) dispatch(payload []byte) error {
	pkt, err := tl.DecodeTCPPacket(payload)
	if err != nil {
		return fmt.Errorf("decode tcp.Packet: %w", err)
	}
	switch pkt.Kind {
	case tl.TCPKindConnected:
		t.inited.Store(true)
		t.logger.Debug("transport: tcp.connected received", "id", pkt.ID)
	case tl.TCPKindConnect:
		// Server-side flow; for client we should not normally receive this.
		t.logger.Warn("transport: unexpected tcp.connect from peer", "id", pkt.ID)
	case tl.TCPKindPing:
		// Reply with pong.
		_ = t.WriteFrame(tl.EncodeTCPPong(pkt.ID))
	case tl.TCPKindPong:
		// Ignore.
	case tl.TCPKindQueryAnswer:
		if !t.corr.deliver(QueryID(pkt.ID), pkt.Data) {
			t.logger.Debug("transport: queryAnswer for unknown id", "id", pkt.ID)
		}
	case tl.TCPKindQueryError:
		err := &ProxyError{Code: int(pkt.ErrCode), Message: pkt.ErrMsg, Phase: "query"}
		if !t.corr.fail(QueryID(pkt.ID), err) {
			t.logger.Debug("transport: queryError for unknown id", "id", pkt.ID, "code", pkt.ErrCode, "msg", pkt.ErrMsg)
		}
	case tl.TCPKindPacket:
		if t.onPacket != nil {
			t.onPacket(pkt.Data)
		}
	case tl.TCPKindQuery:
		// Server-bound; we don't accept queries.
		t.logger.Warn("transport: unexpected tcp.query from peer", "id", pkt.ID)
	default:
		return fmt.Errorf("unknown tcp.Packet kind %d", pkt.Kind)
	}
	return nil
}

// WriteFrame writes a raw framed payload (must be a TL-boxed tcp.Packet).
func (t *transport) WriteFrame(payload []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.frames.WriteFrame(payload)
}

// SendQuery wraps body in tcp.query with a fresh random query_id and writes
// the frame. Returns the channels to await the answer.
func (t *transport) SendQuery(body []byte, timeout time.Duration) (QueryID, <-chan []byte, <-chan error, error) {
	id := newQueryID()
	answer, errCh, err := t.corr.register(id, timeout)
	if err != nil {
		return 0, nil, nil, err
	}
	if err := t.WriteFrame(tl.EncodeTCPQuery(int64(id), body)); err != nil {
		t.corr.fail(id, err)
		return 0, nil, nil, err
	}
	return id, answer, errCh, nil
}

// SendPacket emits a fire-and-forget tcp.packet.
func (t *transport) SendPacket(body []byte) error {
	return t.WriteFrame(tl.EncodeTCPPacket(body))
}

// SendConnect emits the initial tcp.connect (used during handshake).
func (t *transport) SendConnect(id int64) error {
	return t.WriteFrame(tl.EncodeTCPConnect(id))
}

// IsInited reports whether the tcp-layer handshake completed (tcp.connected received).
func (t *transport) IsInited() bool { return t.inited.Load() }

// Closed returns a channel closed when the transport terminates.
func (t *transport) Closed() <-chan struct{} { return t.closed }

// CloseErr returns the cause of close, or nil if still open.
func (t *transport) CloseErr() error {
	if p := t.closeErr.Load(); p != nil {
		return *p
	}
	return nil
}

// Close terminates the transport.
func (t *transport) Close() error {
	t.closeWithErr(errors.New("transport: closed by caller"))
	return nil
}

func (t *transport) closeWithErr(err error) {
	t.closeOnce.Do(func() {
		t.closeErr.Store(&err)
		_ = t.conn.Close()
		t.corr.failAll(err)
		close(t.closed)
	})
}

// StartKeepalive launches a goroutine that sends a tcp.ping every interval
// to prevent the proxy from closing the connection due to inactivity.
//
// Upstream C++ behavior (cocoon/net/TcpConnection.hpp): the proxy (server
// side) closes idle connections after `timeout() = 60s`. The canonical
// client pings at `timeout()/2 = 10s` so that the server keeps resetting
// its idle timer.
//
// Without this loop, gocoon never sends a ping. The proxy times out after
// 60s and forces a reconnect. Each reconnect triggers a long-auth handshake
// that broadcasts an `owner_client_register` on chain and accumulates a
// signed_payment with the proxy, both drain the cocoon wallet and the
// client_sc stake even when the bot is otherwise idle.
//
// Pings are only emitted once the tcp.connected exchange has completed
// (matches upstream `sent_ready_` guard).
func (t *transport) StartKeepalive(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-t.closed:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !t.inited.Load() {
					continue
				}
				id := int64(newQueryID())
				if err := t.WriteFrame(tl.EncodeTCPPing(id)); err != nil {
					t.logger.Debug("transport: keepalive ping failed", "err", err)
					return
				}
				t.logger.Debug("transport: keepalive ping sent", "id", id)
			}
		}
	}()
}

// newQueryID returns a cryptographically-random 64-bit identifier (matches
// upstream td::Random::secure_uint64 usage).
func newQueryID() QueryID {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Cryptographic RNG is not expected to fail on Linux; if it does,
		// we panic, security invariant violated.
		panic(fmt.Sprintf("cocoon: secure rand failed: %v", err))
	}
	return QueryID(int64(binary.LittleEndian.Uint64(b[:])))
}
