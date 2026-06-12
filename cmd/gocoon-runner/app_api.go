package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/TONresistor/gocoon/pkg/cocoon"
	"github.com/TONresistor/gocoon/pkg/setup"
)

// AppAPI serves the desktop/web app endpoints under /api/* on the same port
// as the OpenAI passthrough, so a UI needs exactly one sidecar and one port.
//
//	GET  /api/state          aggregate onboarding + engine snapshot
//	POST /api/wallet/create  generate wallet + config (returns backup once)
//	POST /api/wallet/import  restore from mnemonic or full backup JSON
//	POST /api/wallet/backup  re-read backup bundle
//	GET  /api/wallet/qr.png  funding QR code
//	POST /api/engine/start   start the cocoon engine (requires config)
//	POST /api/engine/stop    stop the cocoon engine
//	GET  /api/logs           recent runner log lines
type AppAPI struct {
	paths  setup.Paths
	engine *Engine
	state  *RunnerState
	logger *slog.Logger
	logs   *logRing
	port   int

	fundMu        sync.Mutex
	fundStatus    *setup.FundingStatus
	fundErr       error
	fundFetchedAt time.Time
}

const fundingCacheTTL = 10 * time.Second

// NewAppAPI wires the app endpoints for a data directory.
func NewAppAPI(dataDir string, port int, engine *Engine, state *RunnerState, logs *logRing, logger *slog.Logger) *AppAPI {
	return &AppAPI{
		paths:  setup.DefaultPaths(dataDir),
		engine: engine,
		state:  state,
		logger: logger,
		logs:   logs,
		port:   port,
	}
}

func (a *AppAPI) routes(mux *http.ServeMux, cors func(http.ResponseWriter)) {
	wrap := func(method string, h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			cors(w)
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.Method != method {
				writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/state", wrap(http.MethodGet, a.handleState))
	mux.HandleFunc("/api/wallet/create", wrap(http.MethodPost, a.handleWalletCreate))
	mux.HandleFunc("/api/wallet/import", wrap(http.MethodPost, a.handleWalletImport))
	mux.HandleFunc("/api/wallet/backup", wrap(http.MethodPost, a.handleWalletBackup))
	mux.HandleFunc("/api/wallet/qr.png", wrap(http.MethodGet, a.handleWalletQR))
	mux.HandleFunc("/api/engine/start", wrap(http.MethodPost, a.handleEngineStart))
	mux.HandleFunc("/api/engine/stop", wrap(http.MethodPost, a.handleEngineStop))
	mux.HandleFunc("/api/logs", wrap(http.MethodGet, a.handleLogs))
}

// appState is the aggregate snapshot the UI polls.
type appState struct {
	Paths     setup.Paths     `json:"paths"`
	HasWallet bool            `json:"has_wallet"`
	HasConfig bool            `json:"has_config"`
	Engine    appEngineState  `json:"engine"`
	Wallet    *appWalletState `json:"wallet,omitempty"`
	WalletErr string          `json:"wallet_error,omitempty"`
	Version   appVersion      `json:"version"`
}

type appEngineState struct {
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

type appWalletState struct {
	OwnerAddress           string `json:"owner_address"`
	FundAddress            string `json:"fund_address"`
	RecommendedFundingNano string `json:"recommended_funding_nano"`
	RecommendedFundingTON  string `json:"recommended_funding_ton"`
	BalanceNano            string `json:"balance_nano,omitempty"`
	BalanceTON             string `json:"balance_ton,omitempty"`
	BalanceSource          string `json:"balance_source,omitempty"`
	BalanceErr             string `json:"balance_error,omitempty"`
	Funded                 *bool  `json:"funded,omitempty"`
	MinClientStakeNano     string `json:"min_client_stake_nano,omitempty"`
	MinClientStakeTON      string `json:"min_client_stake_ton,omitempty"`
}

type appVersion struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

func (a *AppAPI) snapshot(ctx context.Context) appState {
	st := appState{
		Paths:     a.paths,
		HasWallet: setup.FileExists(a.paths.WalletPath),
		HasConfig: setup.FileExists(a.paths.ConfigPath),
		Engine: appEngineState{
			Running: a.engine.Running(),
			Error:   a.engine.LastError(),
		},
		Version: appVersion{Version: cocoon.Version, Commit: cocoon.Commit},
	}
	if !st.HasWallet {
		return st
	}
	info, err := setup.LoadWalletInfo(a.paths.WalletPath)
	if err != nil {
		st.WalletErr = err.Error()
		return st
	}
	wallet := &appWalletState{
		OwnerAddress:           info.OwnerAddress,
		FundAddress:            info.NodeAddress,
		RecommendedFundingNano: strconv.FormatUint(setup.RecommendedFundingNano, 10),
		RecommendedFundingTON:  setup.FormatNanoTON(new(big.Int).SetUint64(setup.RecommendedFundingNano)),
	}
	a.fillBalance(ctx, wallet)
	st.Wallet = wallet
	return st
}

// fillBalance picks the cheapest trustworthy balance source: the discovery
// loop keeps RunnerState.WalletBalance fresh while the engine runs; before
// onboarding completes we poll TON directly (cached).
func (a *AppAPI) fillBalance(ctx context.Context, wallet *appWalletState) {
	if a.engine.Running() {
		a.state.mu.RLock()
		balance := a.state.WalletBalance
		synced := a.state.TONLastSyncedAt
		a.state.mu.RUnlock()
		if synced > 0 {
			setWalletBalance(wallet, new(big.Int).SetUint64(balance), "engine")
			return
		}
	}

	status, err := a.fundingStatusCached(ctx, wallet.FundAddress)
	if err != nil {
		wallet.BalanceErr = err.Error()
		return
	}
	setWalletBalance(wallet, status.BalanceNano, "chain")
	if status.MinClientStakeNano != nil {
		wallet.MinClientStakeNano = status.MinClientStakeNano.String()
		wallet.MinClientStakeTON = setup.FormatNanoTON(status.MinClientStakeNano)
	}
}

func setWalletBalance(wallet *appWalletState, balance *big.Int, source string) {
	if balance == nil {
		balance = big.NewInt(0)
	}
	wallet.BalanceNano = balance.String()
	wallet.BalanceTON = setup.FormatNanoTON(balance)
	wallet.BalanceSource = source
	funded := balance.Cmp(new(big.Int).SetUint64(setup.RecommendedFundingNano)) >= 0
	wallet.Funded = &funded
}

func (a *AppAPI) fundingStatusCached(ctx context.Context, nodeAddress string) (*setup.FundingStatus, error) {
	a.fundMu.Lock()
	defer a.fundMu.Unlock()
	if time.Since(a.fundFetchedAt) < fundingCacheTTL && (a.fundStatus != nil || a.fundErr != nil) {
		return a.fundStatus, a.fundErr
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	a.fundStatus, a.fundErr = setup.FetchFundingStatus(fetchCtx, a.paths.TONConfigPath, nodeAddress)
	a.fundFetchedAt = time.Now()
	return a.fundStatus, a.fundErr
}

func (a *AppAPI) invalidateFundingCache() {
	a.fundMu.Lock()
	a.fundStatus, a.fundErr = nil, nil
	a.fundFetchedAt = time.Time{}
	a.fundMu.Unlock()
}

func (a *AppAPI) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSONValue(w, a.snapshot(r.Context()))
}

func (a *AppAPI) handleWalletCreate(w http.ResponseWriter, r *http.Request) {
	if setup.FileExists(a.paths.WalletPath) && setup.FileExists(a.paths.ConfigPath) {
		writeJSONValue(w, map[string]any{"state": a.snapshot(r.Context())})
		return
	}
	backup, err := setup.Create(a.paths.DataDir, setup.CreateOptions{HTTPPort: a.port})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.invalidateFundingCache()
	a.logger.Info("app: wallet created", "owner", backup.OwnerAddress, "fund", backup.FundAddress)
	writeJSONValue(w, map[string]any{"backup": backup, "state": a.snapshot(r.Context())})
}

func (a *AppAPI) handleWalletImport(w http.ResponseWriter, r *http.Request) {
	if setup.FileExists(a.paths.WalletPath) {
		writeJSONError(w, http.StatusConflict, "wallet already exists; remove it before importing")
		return
	}
	var req struct {
		Mnemonic   string `json:"mnemonic"`
		BackupJSON string `json:"backup_json"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "decode request: "+err.Error())
		return
	}
	opts := setup.CreateOptions{HTTPPort: a.port, Force: true}
	switch {
	case req.BackupJSON != "":
		opts.WalletJSON = []byte(req.BackupJSON)
	case req.Mnemonic != "":
		words, err := setup.NormalizeMnemonic(req.Mnemonic)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		opts.OwnerMnemonic = words
	default:
		writeJSONError(w, http.StatusBadRequest, "mnemonic or backup_json is required")
		return
	}
	backup, err := setup.Create(a.paths.DataDir, opts)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.invalidateFundingCache()
	a.logger.Info("app: wallet imported", "owner", backup.OwnerAddress, "fund", backup.FundAddress)
	writeJSONValue(w, map[string]any{"backup": backup, "state": a.snapshot(r.Context())})
}

func (a *AppAPI) handleWalletBackup(w http.ResponseWriter, r *http.Request) {
	if !setup.FileExists(a.paths.WalletPath) {
		writeJSONError(w, http.StatusBadRequest, "create a wallet first")
		return
	}
	backup, err := setup.ReadBackup(a.paths.WalletPath)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSONValue(w, map[string]any{"backup": backup})
}

func (a *AppAPI) handleWalletQR(w http.ResponseWriter, r *http.Request) {
	if !setup.FileExists(a.paths.WalletPath) {
		writeJSONError(w, http.StatusBadRequest, "create a wallet first")
		return
	}
	info, err := setup.LoadWalletInfo(a.paths.WalletPath)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	payload := "ton://transfer/" + url.PathEscape(info.NodeAddress)
	png, err := qrcode.Encode(payload, qrcode.Medium, 288)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (a *AppAPI) handleEngineStart(w http.ResponseWriter, r *http.Request) {
	if !setup.FileExists(a.paths.ConfigPath) {
		writeJSONError(w, http.StatusBadRequest, "no client-config.json yet; create or import a wallet first")
		return
	}
	if err := a.engine.Start(a.paths.ConfigPath); err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSONValue(w, map[string]any{"state": a.snapshot(r.Context())})
}

func (a *AppAPI) handleEngineStop(w http.ResponseWriter, r *http.Request) {
	a.engine.Stop()
	writeJSONValue(w, map[string]any{"state": a.snapshot(r.Context())})
}

func (a *AppAPI) handleLogs(w http.ResponseWriter, r *http.Request) {
	var lines []string
	if a.logs != nil {
		lines = a.logs.Snapshot()
	}
	writeJSONValue(w, map[string]any{"logs": lines})
}

// writeJSONValue writes v as a JSON response body.
func writeJSONValue(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// logRing keeps the most recent runner log lines for /api/logs.
type logRing struct {
	mu    sync.Mutex
	lines []string
	max   int
	buf   []byte
}

func newLogRing(max int) *logRing {
	return &logRing{max: max}
}

// Write implements io.Writer over line-oriented log output.
func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	for {
		i := bytes.IndexByte(r.buf, '\n')
		if i < 0 {
			break
		}
		line := string(r.buf[:i])
		r.buf = r.buf[i+1:]
		if line == "" {
			continue
		}
		r.lines = append(r.lines, line)
		if len(r.lines) > r.max {
			r.lines = r.lines[len(r.lines)-r.max:]
		}
	}
	return len(p), nil
}

// Snapshot returns a copy of the buffered lines.
func (r *logRing) Snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}
