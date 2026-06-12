package main

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/TONresistor/gocoon/pkg/cocoon"
	"github.com/TONresistor/gocoon/pkg/router"
	"github.com/TONresistor/gocoon/pkg/store"
	storebbolt "github.com/TONresistor/gocoon/pkg/store/bbolt"
	memstore "github.com/TONresistor/gocoon/pkg/store/memory"
	"github.com/xssnick/tonutils-go/address"
)

// Engine owns the cocoon client lifecycle: liteclient stack, persistent
// store, proxy sessions, and background discovery. It can be started and
// stopped while the HTTP control plane keeps serving, which is what lets the
// runner boot before onboarding is complete.
type Engine struct {
	logger  *slog.Logger
	state   *RunnerState
	dataDir string

	mu             sync.Mutex
	cli            *cocoon.Client
	br             *cocoon.LiteclientBroadcaster
	walletAddr     *address.Address
	st             store.Store
	cancelDiscover context.CancelFunc
	running        bool
	lastErr        string
}

// NewEngine returns a stopped engine. dataDir may be empty (store location
// then falls back to $GOCOON_DATA_DIR or the user data dir).
func NewEngine(dataDir string, state *RunnerState, logger *slog.Logger) *Engine {
	return &Engine{logger: logger, state: state, dataDir: dataDir}
}

// Running reports whether the engine is started.
func (e *Engine) Running() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// LastError returns the most recent start failure, if any.
func (e *Engine) LastError() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastErr
}

// Client returns the live cocoon client, or nil when stopped.
func (e *Engine) Client() *cocoon.Client {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cli
}

// Broadcaster returns the external-message broadcaster, or nil when the
// liteclient stack is unavailable.
func (e *Engine) Broadcaster() cocoon.Broadcaster {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.br == nil {
		return nil
	}
	return e.br
}

// WalletAddr returns the cocoon node wallet address, or nil when stopped.
func (e *Engine) WalletAddr() *address.Address {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.walletAddr
}

// Start brings the engine up from a client-config.json. Idempotent: a
// running engine is left untouched.
func (e *Engine) Start(configPath string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return nil
	}

	fail := func(err error) error {
		e.lastErr = err.Error()
		return err
	}

	cfg, err := LoadClientConfig(configPath)
	if err != nil {
		return fail(err)
	}
	keyBytes, err := cfg.NodeKeyBytes()
	if err != nil {
		return fail(err)
	}
	nodeKey := ed25519.NewKeyFromSeed(keyBytes)

	owner, err := address.ParseAddr(cfg.OwnerAddress)
	if err != nil {
		return fail(err)
	}
	rootAddr, err := address.ParseAddr(cfg.RootContractAddr)
	if err != nil {
		return fail(err)
	}

	br, walletAddr, api, err := buildLiteclientStack(cfg, configPath, e.logger)
	if err != nil {
		e.logger.Warn("liteclient init failed (broadcaster offline)", "err", err)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := ensureChainAccess(ctx, api); err != nil {
			e.logger.Warn("liteclient probe failed", "err", err)
		} else {
			e.logger.Info("liteclient: ready", "wallet", walletAddr.String())
		}
		cancel()
	}

	st := openStore(e.dataDir, e.logger)

	clientCfg := cocoon.Config{
		OwnerAddress:        owner,
		NodeKey:             nodeKey,
		RootAddress:         rootAddr,
		LiteClientConfig:    cfg.ResolveTONConfig(configPath),
		Store:               st,
		Router:              &router.DefaultDialer{ClientKey: nodeKey},
		Logger:              e.logger,
		CocoonWalletAddress: walletAddr,
		SecretString:        cfg.SecretString,
	}
	if br != nil {
		clientCfg.Broadcaster = br
	}

	cli, err := cocoon.New(context.Background(), clientCfg)
	if err != nil {
		st.Close()
		return fail(err)
	}

	e.cli = cli
	e.br = br
	e.walletAddr = walletAddr
	e.st = st
	e.running = true
	e.lastErr = ""
	e.state.SetIdentity(cfg.RootContractAddr, cfg.OwnerAddress, true)

	discCtx, discCancel := context.WithCancel(context.Background())
	e.cancelDiscover = discCancel
	if api != nil && walletAddr != nil {
		go runDiscovery(discCtx, cli, api, walletAddr, e.state, e.logger)
	}
	return nil
}

// Stop tears the engine down. Idempotent.
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return
	}
	if e.cancelDiscover != nil {
		e.cancelDiscover()
		e.cancelDiscover = nil
	}
	if e.cli != nil {
		// Client.Close also closes the store it was configured with.
		_ = e.cli.Close()
		e.cli = nil
	}
	e.st = nil
	e.br = nil
	e.walletAddr = nil
	e.running = false
	e.state.SetIdentity("", "", false)
	e.state.SetProxyConnections(nil)
	e.state.SetProxies(nil)
}

// openStore returns a persistent bbolt store rooted at dataDir, then
// $GOCOON_DATA_DIR (set by the browser), then the user's local share dir.
// Falls back to in-memory if no path is writable.
func openStore(dataDir string, logger *slog.Logger) store.Store {
	dir := dataDir
	if dir == "" {
		dir = os.Getenv("GOCOON_DATA_DIR")
	}
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".local", "share", "gocoon")
		}
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err == nil {
			path := filepath.Join(dir, "runner-state.bolt")
			if s, err := storebbolt.Open(path); err == nil {
				logger.Info("store: bbolt", "path", path)
				return s
			}
		}
	}
	logger.Warn("store: falling back to in-memory")
	return memstore.New()
}
