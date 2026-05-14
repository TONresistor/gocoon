package cocoon

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/TONresistor/gocoon/pkg/tl"
)

// handshake encapsulates the multi-step session bring-up used by upstream
// COCOON proxies.
//
// Sequence (after TLS is up, RA-TLS attestation read is in attestation.go):
//
//   1. Client sends tcp.connect with random int64 id.
//   2. Server replies tcp.connected (echo of id is by spec but not enforced).
//   3. Client sends client.connectToProxy (boxed) wrapped in tcp.query.
//   4. Server replies client.connectedToProxy (boxed) inside tcp.queryAnswer.
//   5. Client picks short or long auth based on params.
//   6. Client sends client.authorizeWithProxy{Short|Long} via tcp.query.
//   7. Server replies client.authorizationWithProxy{Success|Failed}.
//   8. On success, session enters Ready.

const (
	handshakeTCPTimeout       = 30 * time.Second
	handshakeAppQueryTimeout  = 30 * time.Second
	handshakeAuthQueryTimeout = 5 * time.Minute
)

// handshakeResult carries everything Session needs after a successful handshake.
type handshakeResult struct {
	ProxyParams       tl.ProxyParams
	ClientSCAddress   string
	ProtoVersion      int32
	SignedPaymentBlob []byte // raw proxy.SignedPayment bytes
	TokensCommittedDB int64
	MaxTokens         int64
}

// runHandshake executes the full sequence on the given transport.
//
// The caller must already have:
//   - opened TLS to the proxy
//   - completed any RA-TLS attestation read (we accept all attestations)
//   - started transport.Run() in a goroutine
//
// runHandshake returns when the session is Ready, or with a typed error.
func runHandshake(
	ctx context.Context,
	tr *transport,
	cocoonWalletAddr string,
	rootConfigVersion int32,
	secretString string,
	registerOnChain func(ctx context.Context, clientSCAddress string, nonce uint64, proxyParams tl.ProxyParams) error,
) (*handshakeResult, error) {
	// ── Step 1+2: tcp.connect / tcp.connected ──────────────────────────────
	connectID := randomInt64()
	if err := tr.SendConnect(connectID); err != nil {
		return nil, fmt.Errorf("%w: send tcp.connect: %v", ErrConnectionFailed, err)
	}
	if err := waitForInited(ctx, tr, handshakeTCPTimeout); err != nil {
		return nil, err
	}

	// ── Step 3+4: client.connectToProxy / client.connectedToProxy ──────────
	connectedToProxy, err := sendConnectToProxy(ctx, tr, cocoonWalletAddr, rootConfigVersion)
	if err != nil {
		return nil, err
	}

	// Validate per ClientProxyConnection.cpp received_handshake_answer.
	if err := validateConnectedToProxy(connectedToProxy); err != nil {
		return nil, err
	}

	// ── Step 5: pick auth path ─────────────────────────────────────────────
	useShort := connectedToProxy.AuthSecretHashOK && len(secretString) > 0

	// ── Step 6+7: authorize ────────────────────────────────────────────────
	var authResult *clientAuthorizationResult
	if useShort {
		authResult, err = sendAuthorizeShort(ctx, tr, secretString)
	} else {
		// Long path: we need to send a register message on chain BEFORE the TL
		// authorize call. The caller passed a registerOnChain closure that
		// performs the cocoon_wallet → proxy SC ext-message broadcast.
		if registerOnChain != nil {
			if err := registerOnChain(ctx, connectedToProxy.ClientSCAddress, connectedToProxy.AuthNonce, connectedToProxy.ProxyParams); err != nil {
				return nil, fmt.Errorf("handshake: on-chain register: %w", err)
			}
		}
		authResult, err = sendAuthorizeLong(ctx, tr)
	}
	if err != nil {
		return nil, err
	}

	if !authResult.Success {
		return nil, &ProxyError{
			Code:    authResult.ErrorCode,
			Message: authResult.ErrorMessage,
			Phase:   "auth",
		}
	}

	return &handshakeResult{
		ProxyParams:       connectedToProxy.ProxyParams,
		ClientSCAddress:   connectedToProxy.ClientSCAddress,
		ProtoVersion:      connectedToProxy.ProxyParams.ProtoVersion,
		SignedPaymentBlob: authResult.SignedPaymentBlob,
		TokensCommittedDB: authResult.TokensCommittedDB,
		MaxTokens:         authResult.MaxTokens,
	}, nil
}

func waitForInited(ctx context.Context, tr *transport, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if tr.IsInited() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tr.Closed():
			if e := tr.CloseErr(); e != nil {
				return fmt.Errorf("%w: %v", ErrConnectionFailed, e)
			}
			return ErrConnectionFailed
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return ErrHandshakeTimeout
		}
	}
}

// connectedToProxyDecoded is the parsed handshake response.
type connectedToProxyDecoded struct {
	ProxyParams       tl.ProxyParams
	ClientSCAddress   string
	AuthSecretHashOK  bool // true if proxy supplied proxyConnectionAuthShort with a hash we'd match
	AuthSecretHash    [32]byte
	AuthNonce         uint64
	SignedPaymentBlob []byte
}

func sendConnectToProxy(ctx context.Context, tr *transport, cocoonWalletAddr string, rootCfgVersion int32) (*connectedToProxyDecoded, error) {
	body := encodeClientConnectToProxy(cocoonWalletAddr, rootCfgVersion)
	answer, err := awaitQuery(ctx, tr, body, handshakeAppQueryTimeout)
	if err != nil {
		return nil, fmt.Errorf("connectToProxy: %w", err)
	}
	return decodeConnectedToProxy(answer)
}

type clientAuthorizationResult struct {
	Success           bool
	ErrorCode         int
	ErrorMessage      string
	SignedPaymentBlob []byte
	TokensCommittedDB int64
	MaxTokens         int64
}

func sendAuthorizeShort(ctx context.Context, tr *transport, secret string) (*clientAuthorizationResult, error) {
	w := tl.NewWriter()
	w.WriteUint32(tl.IDClientAuthorizeWithProxyShort)
	w.WriteBytes([]byte(secret))
	answer, err := awaitQuery(ctx, tr, w.Bytes(), handshakeAuthQueryTimeout)
	if err != nil {
		return nil, fmt.Errorf("authorizeWithProxyShort: %w", err)
	}
	return decodeAuthorizationWithProxy(answer)
}

func sendAuthorizeLong(ctx context.Context, tr *transport) (*clientAuthorizationResult, error) {
	w := tl.NewWriter()
	w.WriteUint32(tl.IDClientAuthorizeWithProxyLong)
	answer, err := awaitQuery(ctx, tr, w.Bytes(), handshakeAuthQueryTimeout)
	if err != nil {
		return nil, fmt.Errorf("authorizeWithProxyLong: %w", err)
	}
	return decodeAuthorizationWithProxy(answer)
}

// awaitQuery sends a TL function body wrapped in tcp.query and blocks until
// the response arrives or the timeout fires.
func awaitQuery(ctx context.Context, tr *transport, body []byte, timeout time.Duration) ([]byte, error) {
	_, answer, errCh, err := tr.SendQuery(body, timeout)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-tr.Closed():
		return nil, ErrConnectionFailed
	case raw := <-answer:
		return raw, nil
	case e := <-errCh:
		return nil, e
	case <-time.After(timeout):
		return nil, ErrRequestTimeout
	}
}

// encodeClientConnectToProxy emits the boxed function body for
// `client.connectToProxy params:client.params min_config_version:int`.
//
// Per upstream ClientProxyConnection.cpp:send_handshake, params has flags=3
// (both is_test and proto_version present), and proto_version range 1..4.
// `params:client.params` is a concrete lower-case TL type, so it is encoded
// bare inside the function body.
func encodeClientConnectToProxy(cocoonWalletAddr string, rootCfgVersion int32) []byte {
	w := tl.NewWriter()
	w.WriteUint32(tl.IDClientConnectToProxy)
	// params (bare):
	w.WriteFlags(0x3)
	w.WriteString(cocoonWalletAddr)
	w.WriteBool(false) // is_test = false (mainnet)
	w.WriteInt32(1)    // min_proto_version
	w.WriteInt32(4)    // max_proto_version
	w.WriteInt32(rootCfgVersion)
	return w.Bytes()
}

// decodeConnectedToProxy parses the boxed client.connectedToProxy response.
//
// Layout: [u32: IDClientConnectedToProxy] [proxy.params bare] [string: client_sc_address]
//
//	[client.ProxyConnectionAuth boxed] [proxy.SignedPayment boxed]
func decodeConnectedToProxy(answer []byte) (*connectedToProxyDecoded, error) {
	r := tl.NewReader(answer)
	id, err := r.ReadUint32()
	if err != nil {
		return nil, err
	}
	if id != tl.IDClientConnectedToProxy {
		return nil, fmt.Errorf("expected client.connectedToProxy (%#x), got %#x: %w",
			tl.IDClientConnectedToProxy, id, tl.ErrUnknownConstructor)
	}
	// params:proxy.params is a concrete lower-case TL type, so it is bare here.
	pp, err := tl.DecodeProxyParamsBody(r)
	if err != nil {
		return nil, fmt.Errorf("proxy.params body: %w", err)
	}
	clientSCAddr, err := r.ReadString()
	if err != nil {
		return nil, err
	}
	authID, err := r.ReadUint32()
	if err != nil {
		return nil, err
	}
	out := &connectedToProxyDecoded{
		ProxyParams:     pp,
		ClientSCAddress: clientSCAddr,
	}
	switch authID {
	case tl.IDClientProxyConnectionAuthShort:
		hash, err := r.ReadInt256()
		if err != nil {
			return nil, err
		}
		nonce, err := r.ReadUint64()
		if err != nil {
			return nil, err
		}
		out.AuthSecretHash = hash
		out.AuthNonce = nonce
		// Whether it's "ours" is decided by Session against runtime secret_hash.
		out.AuthSecretHashOK = false
	case tl.IDClientProxyConnectionAuthLong:
		nonce, err := r.ReadUint64()
		if err != nil {
			return nil, err
		}
		out.AuthNonce = nonce
	default:
		return nil, fmt.Errorf("unknown ProxyConnectionAuth id %#x", authID)
	}
	// proxy.SignedPayment{,Empty}
	spID, err := r.ReadUint32()
	if err != nil {
		return nil, err
	}
	switch spID {
	case tl.IDProxySignedPayment:
		blob, err := r.ReadBytes()
		if err != nil {
			return nil, err
		}
		out.SignedPaymentBlob = blob
	case tl.IDProxySignedPaymentEmpty:
		// no-op
	default:
		return nil, fmt.Errorf("unknown SignedPayment id %#x", spID)
	}
	return out, nil
}

// decodeAuthorizationWithProxy parses Success or Failed.
func decodeAuthorizationWithProxy(answer []byte) (*clientAuthorizationResult, error) {
	r := tl.NewReader(answer)
	id, err := r.ReadUint32()
	if err != nil {
		return nil, err
	}
	switch id {
	case tl.IDClientAuthorizationWithProxySuccess:
		// proxy.SignedPayment boxed | tokens_committed_to_db:long | max_tokens:long
		spID, err := r.ReadUint32()
		if err != nil {
			return nil, err
		}
		var blob []byte
		switch spID {
		case tl.IDProxySignedPayment:
			blob, err = r.ReadBytes()
			if err != nil {
				return nil, err
			}
		case tl.IDProxySignedPaymentEmpty:
			// no-op
		default:
			return nil, fmt.Errorf("authSuccess: unknown SignedPayment %#x", spID)
		}
		tokensDB, err := r.ReadInt64()
		if err != nil {
			return nil, err
		}
		maxT, err := r.ReadInt64()
		if err != nil {
			return nil, err
		}
		return &clientAuthorizationResult{
			Success:           true,
			SignedPaymentBlob: blob,
			TokensCommittedDB: tokensDB,
			MaxTokens:         maxT,
		}, nil
	case tl.IDClientAuthorizationWithProxyFailed:
		code, err := r.ReadInt32()
		if err != nil {
			return nil, err
		}
		msg, err := r.ReadString()
		if err != nil {
			return nil, err
		}
		return &clientAuthorizationResult{
			Success:      false,
			ErrorCode:    int(code),
			ErrorMessage: msg,
		}, nil
	}
	return nil, fmt.Errorf("unknown AuthorizationWithProxy id %#x: %w", id, tl.ErrUnknownConstructor)
}

// validateConnectedToProxy mirrors ClientProxyConnection.cpp validation.
func validateConnectedToProxy(d *connectedToProxyDecoded) error {
	if d.ProxyParams.Flags&0x3 == 0 {
		return fmt.Errorf("%w: proxy too old (flags=%#x)", ErrProtocolMismatch, d.ProxyParams.Flags)
	}
	if d.ProxyParams.HasIsTest() && d.ProxyParams.IsTest {
		return ErrTestModeMismatch
	}
	if d.ProxyParams.HasProtoVersion() {
		if d.ProxyParams.ProtoVersion < 1 || d.ProxyParams.ProtoVersion > 4 {
			return fmt.Errorf("%w: proto_version %d", ErrProtocolMismatch, d.ProxyParams.ProtoVersion)
		}
	}
	return nil
}

func randomInt64() int64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("cocoon: rand: %v", err))
	}
	return int64(binary.LittleEndian.Uint64(b[:]))
}
