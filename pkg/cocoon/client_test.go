package cocoon

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/xssnick/tonutils-go/address"
)

func mustAddr(t *testing.T) *address.Address {
	t.Helper()
	a, err := address.ParseAddr("EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want error
	}{
		{"missing owner", Config{NodeKey: mustKey(t), RootAddress: mustAddr(t)}, ErrInvalidConfig},
		{"missing root", Config{OwnerAddress: mustAddr(t), NodeKey: mustKey(t)}, ErrInvalidConfig},
		{"bad key", Config{OwnerAddress: mustAddr(t), RootAddress: mustAddr(t), NodeKey: ed25519.PrivateKey{1, 2, 3}}, ErrInvalidConfig},
		{"strict policy", Config{OwnerAddress: mustAddr(t), RootAddress: mustAddr(t), NodeKey: mustKey(t), PolicyMode: PolicyStrict}, ErrInvalidConfig},
		{"valid", Config{OwnerAddress: mustAddr(t), RootAddress: mustAddr(t), NodeKey: mustKey(t)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.want == nil {
				if err != nil {
					t.Errorf("got %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Errorf("got %v, want wrap of %v", err, tt.want)
			}
		})
	}
}

func TestNewClientCloseable(t *testing.T) {
	c, err := New(context.Background(), Config{
		OwnerAddress: mustAddr(t),
		RootAddress:  mustAddr(t),
		NodeKey:      mustKey(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Sessions(); len(got) != 0 {
		t.Errorf("fresh client sessions: %d", len(got))
	}
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	// Double-close.
	if err := c.Close(); !errors.Is(err, ErrAlreadyClosed) {
		t.Errorf("double close: %v", err)
	}
}
