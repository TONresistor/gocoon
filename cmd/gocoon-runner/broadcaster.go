package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/TONresistor/gocoon/pkg/cocoon"
	"github.com/TONresistor/gocoon/pkg/contracts/wallet"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// buildLiteclientStack assembles a tonutils-go liteclient pool + API + a
// signing broadcaster from the runner's loaded config.
//
// Returns (broadcaster, cocoon-wallet-address, error).
func buildLiteclientStack(cfg *ClientConfig, configPath string, logger *slog.Logger) (*cocoon.LiteclientBroadcaster, *address.Address, ton.APIClientWrapped, error) {
	keyBytes, err := cfg.NodeKeyBytes()
	if err != nil {
		return nil, nil, nil, err
	}
	nodeKey := ed25519.NewKeyFromSeed(keyBytes)
	pubKey := nodeKey.Public().(ed25519.PublicKey)

	owner, err := address.ParseAddr(cfg.OwnerAddress)
	if err != nil {
		return nil, nil, nil, err
	}

	// Load TON network config.
	tonCfg := cfg.ResolveTONConfig(configPath)
	pool := liteclient.NewConnectionPool()
	if err := pool.AddConnectionsFromConfigFile(tonCfg); err != nil {
		return nil, nil, nil, fmt.Errorf("liteclient: %w", err)
	}
	api := ton.NewAPIClient(pool).WithRetryTimeout(3, 0*time.Second)

	// Load cocoon_wallet code BoC.
	walletCode, err := loadWalletCode()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("wallet code: %w", err)
	}

	// Derive cocoon_wallet address.
	walletAddr, err := wallet.DeriveAddress(wallet.Config{
		PublicKey:    pubKey,
		OwnerAddress: owner,
	}, walletCode)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("wallet derive: %w", err)
	}

	br := cocoon.NewLiteclientBroadcaster(api, nodeKey, walletAddr, owner, walletCode)
	br.Logger = logger
	return br, walletAddr, api, nil
}

// loadWalletCode locates and decodes the cocoon_wallet code BoC bundled with
// the browser (resources/cocoon/cocoon-wallet.code.json) or fallback paths.
//
// Browser-spawned runner: the code lives in
//
//	$RESOURCE_DIR/cocoon/cocoon-wallet.code.json
//
// (electron-builder copies resources/cocoon → cocoon in the app bundle).
//
// Standalone runner: looks at common paths.
func loadWalletCode() (*cell.Cell, error) {
	code, _, err := wallet.LoadDefaultCode()
	return code, err
}

func ensureChainAccess(ctx context.Context, api ton.APIClientWrapped) error {
	if api == nil {
		return errors.New("api: nil")
	}
	_, err := api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return fmt.Errorf("liteclient unreachable: %w", err)
	}
	return nil
}
