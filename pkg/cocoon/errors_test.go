package cocoon

import (
	"errors"
	"testing"
)

func TestProxyErrorUnwrap(t *testing.T) {
	e := &ProxyError{Code: 7, Message: "denied", Phase: "auth"}
	if !errors.Is(e, ErrAuthFailed) {
		t.Errorf("auth ProxyError should unwrap to ErrAuthFailed")
	}
	e2 := &ProxyError{Phase: "query"}
	if !errors.Is(e2, ErrRequestRejected) {
		t.Errorf("query ProxyError should unwrap to ErrRequestRejected")
	}
}

func TestConfigErrorUnwrap(t *testing.T) {
	e := &ConfigError{Field: "node_wallet_key", Reason: "wrong length"}
	if !errors.Is(e, ErrInvalidConfig) {
		t.Errorf("ConfigError should unwrap to ErrInvalidConfig")
	}
}

func TestChainErrorUnwrap(t *testing.T) {
	inner := errors.New("network down")
	e := &ChainError{Op: "broadcast", Inner: inner}
	if !errors.Is(e, inner) {
		t.Errorf("ChainError should unwrap to inner")
	}
}

func TestSentinelsDistinct(t *testing.T) {
	// Sanity: two different sentinels should not be Is-equal.
	if errors.Is(ErrAuthFailed, ErrRequestRejected) {
		t.Error("sentinels conflated")
	}
}
