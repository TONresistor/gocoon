package wallet

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	tonwallet "github.com/xssnick/tonutils-go/ton/wallet"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// Generated is a complete Cocoon wallet bundle.
//
// The JSON field names intentionally match the browser's CocoonWalletData
// storage format, so the output can be consumed by Tonnet Browser without
// lossy field mapping.
type Generated struct {
	OwnerMnemonic    []string `json:"ownerMnemonic"`
	NodeSecretBase64 string   `json:"nodeSecretBase64"`
	NodePublicKeyHex string   `json:"nodePublicKeyHex"`
	OwnerAddress     string   `json:"ownerAddress"`
	NodeAddress      string   `json:"nodeAddress"`
	CreatedAt        int64    `json:"createdAt"`
	SetupCompletedAt *int64   `json:"setupCompletedAt"`
}

// GenerateOptions configures Cocoon wallet generation.
type GenerateOptions struct {
	// Code is the compiled cocoon_wallet smart-contract code cell.
	Code *cell.Cell

	// OwnerMnemonic can be provided to derive a bundle from an existing owner
	// mnemonic. If empty, a fresh 24-word TON mnemonic is generated.
	OwnerMnemonic []string

	// Rand is used for the node Ed25519 seed. Defaults to crypto/rand.Reader.
	Rand io.Reader

	// Now sets CreatedAt. Defaults to time.Now.
	Now func() time.Time
}

// Generate creates the owner TON V4R2 wallet and the Cocoon node wallet data.
func Generate(opts GenerateOptions) (*Generated, error) {
	if opts.Code == nil {
		return nil, fmt.Errorf("wallet: code is nil")
	}

	mnemonic := opts.OwnerMnemonic
	if len(mnemonic) == 0 {
		mnemonic = tonwallet.NewSeed()
	}

	owner, err := tonwallet.FromSeedWithOptions(nil, mnemonic, tonwallet.V4R2)
	if err != nil {
		return nil, fmt.Errorf("wallet: derive owner wallet: %w", err)
	}
	ownerAddress := owner.Address().String()

	r := opts.Rand
	if r == nil {
		r = rand.Reader
	}
	nodeSeed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(r, nodeSeed); err != nil {
		return nil, fmt.Errorf("wallet: generate node seed: %w", err)
	}
	nodeKey := ed25519.NewKeyFromSeed(nodeSeed)
	nodePublic := nodeKey.Public().(ed25519.PublicKey)

	nodeAddress, err := DeriveAddress(Config{
		PublicKey:    nodePublic,
		OwnerAddress: owner.Address(),
	}, opts.Code)
	if err != nil {
		return nil, fmt.Errorf("wallet: derive cocoon wallet: %w", err)
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Generated{
		OwnerMnemonic:    append([]string(nil), mnemonic...),
		NodeSecretBase64: base64.StdEncoding.EncodeToString(nodeSeed),
		NodePublicKeyHex: hex.EncodeToString(nodePublic),
		OwnerAddress:     ownerAddress,
		NodeAddress:      nodeAddress.String(),
		CreatedAt:        now().UnixMilli(),
		SetupCompletedAt: nil,
	}, nil
}
