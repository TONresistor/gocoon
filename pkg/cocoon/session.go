package cocoon

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/TONresistor/gocoon/pkg/tl"
)

// Session is a logical TL session over one TLS connection to one proxy.
type Session struct {
	client    *Client
	proxyAddr string

	mu           sync.RWMutex
	conn         net.Conn
	transport    *transport
	status       SessionStatus
	params       tl.ProxyParams
	clientSCAddr string
	err          error
	hsResult     *handshakeResult

	// streaming dispatch: per-request channels indexed by request_id [32]byte
	streams map[[32]byte]*streamChannel

	closeOnce sync.Once
	done      chan struct{}
}

func newSession(c *Client, proxyAddr string) *Session {
	return &Session{
		client:    c,
		proxyAddr: proxyAddr,
		status:    SessionConnecting,
		streams:   make(map[[32]byte]*streamChannel),
		done:      make(chan struct{}),
	}
}

// ProxyAddress returns the upstream proxy network address (host:port).
func (s *Session) ProxyAddress() string { return s.proxyAddr }

// Status returns the current session status.
func (s *Session) Status() SessionStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *Session) isUsable() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.status != SessionReady || s.transport == nil {
		return false
	}
	select {
	case <-s.transport.Closed():
		return false
	default:
		return true
	}
}

// Params returns the proxy.Params received at handshake.
func (s *Session) Params() tl.ProxyParams {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.params
}

// ClientSCAddress returns the cocoon_client SC address learned at handshake.
func (s *Session) ClientSCAddress() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.clientSCAddr
}

// Wait blocks until the session is Ready or ctx is done.
func (s *Session) Wait(ctx context.Context) error {
	for {
		st := s.Status()
		if st == SessionReady {
			return nil
		}
		if st == SessionDisconnected || st == SessionCrashed {
			s.mu.RLock()
			err := s.err
			s.mu.RUnlock()
			if err != nil {
				return err
			}
			return ErrNotConnected
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return ErrNotConnected
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// connect runs the handshake state machine.
func (s *Session) connect(ctx context.Context) error {
	dialer := s.client.router
	conn, err := dialer.DialProxy(ctx, s.proxyAddr)
	if err != nil {
		s.fail(fmt.Errorf("%w: %v", ErrConnectionFailed, err))
		return err
	}

	corr := newCorrelator()
	tr := newTransport(conn, corr, s.client.logger)
	tr.onPacket = s.dispatchPacket

	s.mu.Lock()
	s.conn = conn
	s.transport = tr
	s.mu.Unlock()

	// Run transport read loop and gc in background.
	bgCtx, cancel := context.WithCancel(context.Background())
	go func() {
		<-s.done
		cancel()
	}()
	go tr.Run(bgCtx)
	go corr.runGC(bgCtx, time.Second)

	// Run handshake.
	cocoonWalletAddr := s.client.cocoonWalletAddrStr()
	rootCfgVersion := s.client.rootConfigVersion()
	res, err := runHandshake(ctx, tr, cocoonWalletAddr, rootCfgVersion, s.client.cfg.SecretString, s.client.registerOnChain)
	if err != nil {
		s.fail(err)
		_ = tr.Close()
		return err
	}

	s.mu.Lock()
	s.params = res.ProxyParams
	s.clientSCAddr = res.ClientSCAddress
	s.hsResult = res
	s.status = SessionReady
	s.mu.Unlock()
	s.client.logger.Info("cocoon: session ready",
		"proxy", s.proxyAddr,
		"client_sc", res.ClientSCAddress,
		"proto_version", res.ProtoVersion,
	)
	go s.removeOnTransportClose(tr)
	return nil
}

func (s *Session) removeOnTransportClose(tr *transport) {
	<-tr.Closed()
	s.mu.RLock()
	current := s.transport == tr
	status := s.status
	s.mu.RUnlock()
	if !current || status != SessionReady {
		return
	}
	err := tr.CloseErr()
	s.client.logger.Warn("cocoon: session transport closed", "proxy", s.proxyAddr, "err", err)
	s.client.removeSession(s.proxyAddr)
}

// Close terminates the session.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		tr := s.transport
		s.transport = nil
		s.conn = nil
		s.status = SessionDisconnected
		s.mu.Unlock()
		if tr != nil {
			_ = tr.Close()
		}
		close(s.done)
	})
	return nil
}

func (s *Session) fail(err error) {
	s.mu.Lock()
	s.status = SessionCrashed
	s.err = err
	s.mu.Unlock()
}

// PaymentStatus calls client.updatePaymentStatus on the proxy.
func (s *Session) PaymentStatus(ctx context.Context) (PaymentStatus, error) {
	if s.Status() != SessionReady {
		return PaymentStatus{}, ErrNotConnected
	}
	s.mu.RLock()
	tr := s.transport
	s.mu.RUnlock()
	if tr == nil {
		return PaymentStatus{}, ErrNotConnected
	}
	w := tl.NewWriter()
	w.WriteUint32(tl.IDClientUpdatePaymentStatus)
	answer, err := awaitQuery(ctx, tr, w.Bytes(), 30*time.Second)
	if err != nil {
		return PaymentStatus{}, err
	}
	return decodePaymentStatus(answer)
}

func decodePaymentStatus(answer []byte) (PaymentStatus, error) {
	r := tl.NewReader(answer)
	id, err := r.ReadUint32()
	if err != nil {
		return PaymentStatus{}, err
	}
	if id != tl.IDClientPaymentStatus {
		return PaymentStatus{}, fmt.Errorf("expected client.paymentStatus (%#x), got %#x", tl.IDClientPaymentStatus, id)
	}
	// proxy.SignedPayment boxed | db_tokens:long | max_tokens:long
	spID, err := r.ReadUint32()
	if err != nil {
		return PaymentStatus{}, err
	}
	switch spID {
	case tl.IDProxySignedPayment:
		if _, err := r.ReadBytes(); err != nil {
			return PaymentStatus{}, err
		}
	case tl.IDProxySignedPaymentEmpty:
		// nothing
	default:
		return PaymentStatus{}, fmt.Errorf("paymentStatus: unknown SignedPayment %#x", spID)
	}
	dbTokens, err := r.ReadInt64()
	if err != nil {
		return PaymentStatus{}, err
	}
	maxTokens, err := r.ReadInt64()
	if err != nil {
		return PaymentStatus{}, err
	}
	return PaymentStatus{
		TokensCommittedToDB: dbTokens,
		TokensMax:           maxTokens,
	}, nil
}

// ChannelOps returns the per-session channel operations helper.
func (s *Session) ChannelOps() (*ChannelOps, error) {
	if s.Status() != SessionReady {
		return nil, ErrNotConnected
	}
	s.mu.RLock()
	clientSCStr := s.clientSCAddr
	s.mu.RUnlock()
	if clientSCStr == "" {
		return nil, ErrNotConnected
	}
	ops, err := s.client.newChannelOps(clientSCStr)
	if err != nil {
		return nil, err
	}
	return ops, nil
}
