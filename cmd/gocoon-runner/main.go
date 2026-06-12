// Command gocoon-runner is the browser-compat runner binary, drop-in
// replacement for upstream cocoon-runner.
//
// Modes:
//
//	gocoon-runner --config client-config.json   classic: engine starts now
//	gocoon-runner --data-dir <dir>              app: HTTP comes up first; the
//	                                            /api/* endpoints handle wallet
//	                                            onboarding and engine start
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/TONresistor/gocoon/pkg/core"
	"github.com/TONresistor/gocoon/pkg/cocoon"
	"github.com/TONresistor/gocoon/pkg/setup"
)

func main() {
	var (
		configPath  string
		dataDir     string
		verbosity   int
		showVersion bool
	)
	flag.StringVar(&configPath, "config", "", "path to client-config.json")
	flag.StringVar(&configPath, "c", "", "shorthand for --config")
	flag.StringVar(&dataDir, "data-dir", "", "Cocoon data directory (enables /api onboarding endpoints)")
	flag.IntVar(&verbosity, "v", 1, "verbosity (0..3)")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	preprocessConcatVFlag()
	flag.Parse()

	if showVersion {
		fmt.Printf("gocoon-runner %s (%s, built %s)\n", cocoon.Version, cocoon.Commit, cocoon.BuildDate)
		return
	}
	if configPath == "" && dataDir == "" {
		fmt.Fprintln(os.Stderr, "gocoon-runner: --config or --data-dir is required")
		os.Exit(2)
	}

	logs := newLogRing(1000)
	logger := newLogger(verbosity, logs)
	if err := run(configPath, dataDir, logs, logger); err != nil {
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

func newLogger(verbosity int, logs *logRing) *slog.Logger {
	level := slog.LevelInfo
	switch {
	case verbosity <= 0:
		level = slog.LevelError
	case verbosity == 1:
		level = slog.LevelInfo
	case verbosity >= 2:
		level = slog.LevelDebug
	}
	var out io.Writer = os.Stderr
	if logs != nil {
		out = io.MultiWriter(os.Stderr, logs)
	}
	h := slog.NewTextHandler(out, &slog.HandlerOptions{Level: level})
	return slog.New(h)
}

func run(configPath, dataDir string, logs *logRing, logger *slog.Logger) error {
	appMode := dataDir != ""
	if appMode && configPath == "" {
		configPath = setup.DefaultPaths(dataDir).ConfigPath
	}
	if !appMode {
		// Classic mode: store lives next to GOCOON_DATA_DIR or user dir, and
		// the data dir for /api purposes is the config's directory.
		dataDir = os.Getenv("GOCOON_DATA_DIR")
		if dataDir == "" {
			dataDir = filepath.Dir(configPath)
		}
	}

	// Port: from config when present, default 10000 before onboarding.
	port := 10000
	configExists := setup.FileExists(configPath)
	if configExists {
		cfg, err := core.LoadClientConfig(configPath)
		if err != nil {
			return err
		}
		port = cfg.HTTPPort
	} else if !appMode {
		return fmt.Errorf("config: read %s: no such file", configPath)
	}

	state := &core.RunnerState{
		GitCommit:        cocoon.Commit,
		CheckImageHashes: false,
	}
	engine := core.NewEngine(dataDir, state, logger)
	defer engine.Stop()

	cp := NewControlPlane(port, engine, state, logger)
	cp.App = NewAppAPI(dataDir, port, engine, state, logs, logger)
	if err := cp.Start(); err != nil {
		return err
	}

	if configExists {
		if err := engine.Start(configPath); err != nil {
			if !appMode {
				return err
			}
			// App mode keeps serving so the UI can show the error and retry.
			logger.Error("engine start failed", "err", err)
		}
	} else {
		logger.Info("no client-config.json yet; waiting for onboarding via /api")
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
