package cocoon

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by the public Cocoon API.
var (
	// Configuration
	ErrInvalidConfig       = errors.New("cocoon: invalid configuration")
	ErrMissingNodeKey      = errors.New("cocoon: node_wallet_key missing or wrong length")
	ErrMissingOwnerAddress = errors.New("cocoon: owner_address missing or unparseable")
	ErrUnsupportedNetwork  = errors.New("cocoon: testnet not supported in v1")

	// Discovery
	ErrRootContractUnreachable = errors.New("cocoon: cannot reach cocoon_root on TON")
	ErrRootDecode              = errors.New("cocoon: cannot decode cocoon_root storage")
	ErrProxyUnknown            = errors.New("cocoon: proxy not registered in root contract")

	// Connection / handshake
	ErrConnectionFailed = errors.New("cocoon: tcp connection failed")
	ErrTLSHandshake     = errors.New("cocoon: tls handshake failed")
	ErrHandshakeTimeout = errors.New("cocoon: handshake did not complete within timeout")
	ErrProtocolMismatch = errors.New("cocoon: protocol version not in supported range [1..4]")
	ErrTestModeMismatch = errors.New("cocoon: proxy reports different is_test value")

	// Auth
	ErrAuthFailed = errors.New("cocoon: authorization rejected by proxy")

	// Channel / payments
	ErrInsufficientBalance = errors.New("cocoon: client SC balance below stake threshold")
	ErrChannelClosed       = errors.New("cocoon: channel state == closed")
	ErrChannelClosing      = errors.New("cocoon: channel state == closing, refund pending")
	ErrStakeBelowMin       = errors.New("cocoon: stake below min_client_stake from root config")

	// Inference
	ErrRequestRejected = errors.New("cocoon: proxy rejected the request")
	ErrRequestTimeout  = errors.New("cocoon: request did not complete within timeout")
	ErrModelUnknown    = errors.New("cocoon: requested model not advertised by any proxy")

	// State
	ErrNotConnected  = errors.New("cocoon: session not connected")
	ErrAlreadyClosed = errors.New("cocoon: client already closed")

	// Schema versioning
	ErrIncompatibleSchema = errors.New("cocoon: wire schema version not supported")

	// Feature flags
	ErrUnsupported = errors.New("cocoon: feature not implemented in this version")
)

// ProxyError is a structured error returned by upstream proxies.
type ProxyError struct {
	Code      int
	Message   string
	Phase     string
	RequestID *[32]byte
}

func (e *ProxyError) Error() string {
	if e.RequestID != nil {
		return fmt.Sprintf("cocoon: proxy error in phase %q: code=%d msg=%q", e.Phase, e.Code, e.Message)
	}
	return fmt.Sprintf("cocoon: proxy error in phase %q: code=%d msg=%q", e.Phase, e.Code, e.Message)
}

// Unwrap maps to a sentinel for `errors.Is` dispatch.
func (e *ProxyError) Unwrap() error {
	switch e.Phase {
	case "auth":
		return ErrAuthFailed
	case "query":
		return ErrRequestRejected
	default:
		return ErrRequestRejected
	}
}

// ConfigError is a validation failure on a known config field.
type ConfigError struct {
	Field  string
	Reason string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("cocoon: invalid config field %q: %s", e.Field, e.Reason)
}

func (e *ConfigError) Unwrap() error { return ErrInvalidConfig }

// ChainError captures errors from on-chain operations.
type ChainError struct {
	Op      string // "broadcast" | "get_method" | "get_account"
	BocHash string // populated when broadcast was attempted
	Inner   error
}

func (e *ChainError) Error() string {
	if e.BocHash != "" {
		return fmt.Sprintf("cocoon: chain op %s (boc=%s): %v", e.Op, e.BocHash, e.Inner)
	}
	return fmt.Sprintf("cocoon: chain op %s: %v", e.Op, e.Inner)
}

func (e *ChainError) Unwrap() error { return e.Inner }
