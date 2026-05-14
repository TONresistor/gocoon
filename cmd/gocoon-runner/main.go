// Command gocoon-runner is the browser-compat runner binary, drop-in
// replacement for upstream cocoon-runner.
package main

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/TONresistor/gocoon/pkg/cocoon"
	"github.com/TONresistor/gocoon/pkg/router"
	"github.com/TONresistor/gocoon/pkg/store"
	storebbolt "github.com/TONresistor/gocoon/pkg/store/bbolt"
	memstore "github.com/TONresistor/gocoon/pkg/store/memory"
	"github.com/xssnick/tonutils-go/address"
)

func main() {
	var (
		configPath  string
		verbosity   int
		showVersion bool
	)
	flag.StringVar(&configPath, "config", "", "path to client-config.json")
	flag.StringVar(&configPath, "c", "", "shorthand for --config")
	flag.IntVar(&verbosity, "v", 1, "verbosity (0..3)")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	preprocessConcatVFlag()
	flag.Parse()

	if showVersion {
		fmt.Printf("gocoon-runner %s (%s, built %s)\n", cocoon.Version, cocoon.Commit, cocoon.BuildDate)
		return
	}
	if configPath == "" {
		fmt.Fprintln(os.Stderr, "gocoon-runner: --config is required")
		os.Exit(2)
	}

	logger := newLogger(verbosity)
	if err := run(configPath, logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// preprocessConcatVFlag rewrites "-vN" / "-v=N" compact forms into the
// "-v=N" form that Go's flag package expects. Upstream cocoon-runner accepts
// "-v3" as the third-arity verbosity flag; we mirror that.
func preprocessConcatVFlag() {
	out := []string{os.Args[0]}
	for _, a := range os.Args[1:] {
		if len(a) > 2 && a[0] == '-' && a[1] == 'v' && a[2] != '=' && a[2] != '-' {
			rest := a[2:]
			allDigit := len(rest) > 0
			for _, c := range rest {
				if c < '0' || c > '9' {
					allDigit = false
					break
				}
			}
			if allDigit {
				out = append(out, "-v="+rest)
				continue
			}
		}
		out = append(out, a)
	}
	os.Args = out
}

func newLogger(verbosity int) *slog.Logger {
	level := slog.LevelInfo
	switch {
	case verbosity <= 0:
		level = slog.LevelError
	case verbosity == 1:
		level = slog.LevelInfo
	case verbosity == 2:
		level = slog.LevelDebug
	case verbosity >= 3:
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	return slog.New(h)
}

// openStore returns a persistent bbolt store rooted at $GOCOON_DATA_DIR (set
// by the browser) or $HOME/.local/share/gocoon for standalone runs.
// Falls back to in-memory if neither path is writable.
func openStore(logger *slog.Logger) store.Store {
	dir := os.Getenv("GOCOON_DATA_DIR")
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

func run(configPath string, logger *slog.Logger) error {
	cfg, err := LoadClientConfig(configPath)
	if err != nil {
		return err
	}

	keyBytes, err := cfg.NodeKeyBytes()
	if err != nil {
		return err
	}
	nodeKey := ed25519.NewKeyFromSeed(keyBytes)

	owner, err := address.ParseAddr(cfg.OwnerAddress)
	if err != nil {
		return err
	}
	rootAddr, err := address.ParseAddr(cfg.RootContractAddr)
	if err != nil {
		return err
	}

	br, walletAddr, api, err := buildLiteclientStack(cfg, configPath, logger)
	if err != nil {
		logger.Warn("liteclient init failed (broadcaster offline)", "err", err)
	} else {
		// Probe chain access early , surface failure in logs.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := ensureChainAccess(ctx, api); err != nil {
			logger.Warn("liteclient probe failed", "err", err)
		} else {
			logger.Info("liteclient: ready", "wallet", walletAddr.String())
		}
		cancel()
	}

	st := openStore(logger)

	// Dial proxies directly with full PoW + TLS. No separate router process
	// is needed for the Go runner path.
	clientCfg := cocoon.Config{
		OwnerAddress:        owner,
		NodeKey:             nodeKey,
		RootAddress:         rootAddr,
		LiteClientConfig:    cfg.ResolveTONConfig(configPath),
		Store:               st,
		Router:              &router.DefaultDialer{ClientKey: nodeKey},
		Logger:              logger,
		CocoonWalletAddress: walletAddr,
		SecretString:        cfg.SecretString,
	}
	if br != nil {
		clientCfg.Broadcaster = br
	}

	cli, err := cocoon.New(context.Background(), clientCfg)
	if err != nil {
		return err
	}
	defer cli.Close()

	state := &RunnerState{
		Enabled:          true,
		GitCommit:        cocoon.Commit,
		RootAddress:      cfg.RootContractAddr,
		OwnerAddress:     cfg.OwnerAddress,
		CheckImageHashes: false,
		TONLastSyncedAt:  time.Now().Unix(),
	}
	cp := NewControlPlane(cfg.HTTPPort, cli, state, logger)
	cp.Broadcaster = br
	if walletAddr != nil {
		cp.CocoonWalletAddr = walletAddr
	}
	if err := cp.Start(); err != nil {
		return err
	}

	// Background discovery updates wallet/root state and publishes a proxy
	// entry only after a live session is actually ready.
	discCtx, discCancel := context.WithCancel(context.Background())
	defer discCancel()
	if api != nil && walletAddr != nil {
		go runDiscovery(discCtx, cli, api, walletAddr, state, logger)
	}

	// Block on signal.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigs
	logger.Info("signal received, shutting down", "signal", sig.String())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cp.Shutdown(ctx)
}
