// Package fakeproxy implements an in-process fake COCOON proxy that speaks
// the same wire protocol as the real upstream proxy. Used for integration
// tests of pkg/cocoon without network or live mainnet.
package fakeproxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"

	"github.com/TONresistor/gocoon/pkg/tl"
)

// Server is the fake proxy. Listen on a localhost port, accept one client
// at a time, run the documented handshake, and dispatch a configurable
// query handler.
type Server struct {
	ln net.Listener
	// QueryHandler is invoked with each client.connectToProxy → ok or fail.
	// Default returns success with empty signed_payment.
	OnAuth func(secretBlob []byte) (success bool, errCode int, errMsg string)
	// OnQuery is invoked when a tcp.query carries a TL function we want to
	// answer. Return the boxed answer payload bytes (or an error to send
	// tcp.queryError).
	OnQuery func(funcID uint32, body []byte) ([]byte, error)

	closeOnce sync.Once
	wg        sync.WaitGroup
}

// Start spawns the listener; returns the chosen address.
func Start(ctx context.Context) (*Server, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	s := &Server{ln: ln}
	s.wg.Add(1)
	go s.acceptLoop(ctx)
	return s, ln.Addr().String(), nil
}

// Stop closes the listener and waits for accept loop to drain.
func (s *Server) Stop() {
	s.closeOnce.Do(func() {
		_ = s.ln.Close()
	})
	s.wg.Wait()
}

func (s *Server) acceptLoop(ctx context.Context) {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	frames := tl.NewFramedConn(conn)
	frames.Strict = true

	// Wait for tcp.connect, reply with tcp.connected.
	payload, err := frames.ReadFrame()
	if err != nil {
		return
	}
	pkt, err := tl.DecodeTCPPacket(payload)
	if err != nil || pkt.Kind != tl.TCPKindConnect {
		return
	}
	if err := frames.WriteFrame(tl.EncodeTCPConnected(pkt.ID)); err != nil {
		return
	}

	// Now serve queries.
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		raw, err := frames.ReadFrame()
		if err != nil {
			return
		}
		pkt, err := tl.DecodeTCPPacket(raw)
		if err != nil {
			return
		}
		switch pkt.Kind {
		case tl.TCPKindQuery:
			s.handleQuery(frames, pkt.ID, pkt.Data)
		case tl.TCPKindPing:
			_ = frames.WriteFrame(tl.EncodeTCPPong(pkt.ID))
		default:
			// Ignore.
		}
	}
}

// handleQuery dispatches a tcp.query to the appropriate canned response.
func (s *Server) handleQuery(frames *tl.FramedConn, queryID int64, body []byte) {
	if len(body) < 4 {
		return
	}
	funcID := binary.LittleEndian.Uint32(body[:4])

	answer, err := s.synthesizeAnswer(funcID, body)
	if err != nil {
		w := tl.NewWriter()
		w.WriteUint32(tl.IDTCPQueryError)
		w.WriteInt64(queryID)
		w.WriteInt32(13)
		w.WriteString(err.Error())
		_ = frames.WriteFrame(w.Bytes())
		return
	}
	w := tl.NewWriter()
	w.WriteUint32(tl.IDTCPQueryAnswer)
	w.WriteInt64(queryID)
	w.WriteBytes(answer)
	_ = frames.WriteFrame(w.Bytes())
}

func (s *Server) synthesizeAnswer(funcID uint32, body []byte) ([]byte, error) {
	switch funcID {
	case tl.IDClientConnectToProxy:
		// Reply with a minimal client.connectedToProxy.
		w := tl.NewWriter()
		w.WriteUint32(tl.IDClientConnectedToProxy)
		// proxy.params is encoded bare inside client.connectedToProxy.
		w.WriteFlags(0x3)
		var pk [32]byte
		w.WriteInt256(pk)
		// Use a syntactically valid TON mainnet address so consumers that
		// parse it via address.ParseAddr succeed.
		const realAddr = "EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k"
		w.WriteString(realAddr) // proxy_owner_address
		w.WriteString(realAddr) // proxy_sc_address
		w.WriteBool(false)      // is_test
		w.WriteInt32(3)         // proto_version
		w.WriteString(realAddr) // client_sc_address
		// auth: long path (we don't track secrets in fake)
		w.WriteUint32(tl.IDClientProxyConnectionAuthLong)
		w.WriteUint64(0xdeadbeefcafe)
		// signed_payment: empty
		w.WriteUint32(tl.IDProxySignedPaymentEmpty)
		return w.Bytes(), nil

	case tl.IDClientAuthorizeWithProxyLong, tl.IDClientAuthorizeWithProxyShort:
		// Reply success by default.
		var success bool
		var code int
		var msg string
		if s.OnAuth != nil {
			r := tl.NewReader(body)
			_, _ = r.ReadUint32()
			secretBlob, _ := r.ReadBytes()
			success, code, msg = s.OnAuth(secretBlob)
		} else {
			success = true
		}
		w := tl.NewWriter()
		if success {
			w.WriteUint32(tl.IDClientAuthorizationWithProxySuccess)
			w.WriteUint32(tl.IDProxySignedPaymentEmpty)
			w.WriteInt64(0)       // tokens_committed_to_db
			w.WriteInt64(1000000) // max_tokens
		} else {
			w.WriteUint32(tl.IDClientAuthorizationWithProxyFailed)
			w.WriteInt32(int32(code))
			w.WriteString(msg)
		}
		return w.Bytes(), nil

	case tl.IDClientUpdatePaymentStatus:
		w := tl.NewWriter()
		w.WriteUint32(tl.IDClientPaymentStatus)
		w.WriteUint32(tl.IDProxySignedPaymentEmpty)
		w.WriteInt64(0)
		w.WriteInt64(1000000)
		return w.Bytes(), nil
	}

	if s.OnQuery != nil {
		return s.OnQuery(funcID, body)
	}
	return nil, fmt.Errorf("fakeproxy: unsupported function %#x", funcID)
}
