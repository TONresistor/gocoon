package wallet

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"

	"github.com/xssnick/tonutils-go/tvm/cell"
)

func TestGenerateCocoonWalletBundleShape(t *testing.T) {
	code := cell.BeginCell().MustStoreUInt(0x1234, 16).EndCell()
	nodeSeed := bytes.Repeat([]byte{7}, 32)
	got, err := Generate(GenerateOptions{
		Code: code,
		Rand: bytes.NewReader(nodeSeed),
		Now:  func() time.Time { return time.UnixMilli(1_700_000_000_123) },
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got.OwnerMnemonic) != 24 {
		t.Fatalf("owner mnemonic length: %d, want 24", len(got.OwnerMnemonic))
	}
	if got.OwnerAddress == "" || got.NodeAddress == "" {
		t.Fatalf("missing addresses: owner=%q node=%q", got.OwnerAddress, got.NodeAddress)
	}
	if got.CreatedAt != 1_700_000_000_123 {
		t.Fatalf("createdAt: %d", got.CreatedAt)
	}
	if got.SetupCompletedAt != nil {
		t.Fatalf("setupCompletedAt should be nil")
	}

	secret, err := base64.StdEncoding.DecodeString(got.NodeSecretBase64)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(secret, nodeSeed) {
		t.Fatalf("node secret mismatch")
	}
	pub, err := hex.DecodeString(got.NodePublicKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != 32 {
		t.Fatalf("node public key length: %d, want 32", len(pub))
	}
}
