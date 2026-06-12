package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/TONresistor/gocoon/pkg/contracts/client"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// ControlPlane exposes the runner's HTTP control plane on 127.0.0.1.
//
// Endpoints:
//
//	GET /jsonstats               JSON snapshot
//	GET /request/close           HTML, triggers owner_client_request_refund
//	GET /request/withdraw        HTML, triggers owner_client_withdraw
//	GET /request/topup           HTML, triggers ext_client_top_up
//	GET /stats                   HTML overview (debug)
type ControlPlane struct {
	port   int
	engine *Engine
	state  *RunnerState
	logger *slog.Logger

	// App, when set, mounts the /api/* onboarding and lifecycle endpoints.
	App *AppAPI

	server  *http.Server
	mu      sync.Mutex
	started time.Time
}

// RunnerState is what the runner exposes to /jsonstats. Updated by the
// session manager; read by the HTTP handler.
type RunnerState struct {
	mu               sync.RWMutex
	WalletBalance    uint64
	TONLastSyncedAt  int64
	Enabled          bool
	GitCommit        string
	RootAddress      string
	OwnerAddress     string
	CheckImageHashes bool
	ProxyConnections []ProxyConnectionEntry
	Proxies          []ProxyStatEntry
}

// SetProxyConnections atomically replaces the proxy_connections list.
func (s *RunnerState) SetProxyConnections(entries []ProxyConnectionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ProxyConnections = entries
}

// SetProxies atomically replaces the proxies list.
func (s *RunnerState) SetProxies(entries []ProxyStatEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Proxies = entries
}

// SetSyncTime updates ton_last_synced_at.
func (s *RunnerState) SetSyncTime(t int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TONLastSyncedAt = t
}

// SetWalletBalance updates the wallet balance shown in /jsonstats.
func (s *RunnerState) SetWalletBalance(b uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.WalletBalance = b
}

// SetIdentity updates the engine identity exposed in /jsonstats when the
// engine starts or stops.
func (s *RunnerState) SetIdentity(rootAddress, ownerAddress string, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RootAddress = rootAddress
	s.OwnerAddress = ownerAddress
	s.Enabled = enabled
}

// ProxyConnectionEntry mirrors the JSON shape the browser parses.
type ProxyConnectionEntry struct {
	Address        string `json:"address"`
	IsReady        bool   `json:"is_ready"`
	ProxySCAddress string `json:"proxy_sc_address"`
}

// ProxyStatEntry mirrors RunnerProxyStat in the browser TS interface.
type ProxyStatEntry struct {
	ProxySCAddress                       string `json:"proxy_sc_address"`
	ProxyPublicKey                       string `json:"proxy_public_key"`
	SCAddress                            string `json:"sc_address"`
	State                                int    `json:"state"`
	TokensUsedProxyCommittedToBlockchain int64  `json:"tokens_used_proxy_committed_to_blockchain"`
	TokensUsedProxyCommittedToDB         int64  `json:"tokens_used_proxy_committed_to_db"`
	TokensUsedProxyMax                   int64  `json:"tokens_used_proxy_max"`
	TokensCharged                        int64  `json:"tokens_charged"`
	TokensPayed                          int64  `json:"tokens_payed"`
}

// jsonStatsResponse is the exact wire shape the browser parses.
type jsonStatsResponse struct {
	Status           jsonStatsStatus        `json:"status"`
	LocalConf        jsonStatsLocalConf     `json:"localconf"`
	ProxyConnections []ProxyConnectionEntry `json:"proxy_connections"`
	Proxies          []ProxyStatEntry       `json:"proxies"`
}

type jsonStatsStatus struct {
	WalletBalance   uint64 `json:"wallet_balance"`
	TONLastSyncedAt int64  `json:"ton_last_synced_at"`
	Enabled         bool   `json:"enabled"`
	GitCommit       string `json:"git_commit"`
}

type jsonStatsLocalConf struct {
	RootAddress      string `json:"root_address"`
	OwnerAddress     string `json:"owner_address"`
	CheckImageHashes bool   `json:"check_image_hashes"`
}

// NewControlPlane returns a configured ControlPlane.
func NewControlPlane(port int, engine *Engine, state *RunnerState, logger *slog.Logger) *ControlPlane {
	return &ControlPlane{port: port, engine: engine, state: state, logger: logger}
}

// Start begins serving on 127.0.0.1:<port>.
func (cp *ControlPlane) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/jsonstats", cp.handleJSONStats)
	mux.HandleFunc("/request/close", cp.requestHandler("close"))
	mux.HandleFunc("/request/withdraw", cp.requestHandler("withdraw"))
	mux.HandleFunc("/request/topup", cp.requestHandler("topup"))
	mux.HandleFunc("/v1/chat/completions", cp.handleChatCompletions)
	mux.HandleFunc("/v1/models", cp.handleModels)
	mux.HandleFunc("/stats", cp.handleStats)
	mux.HandleFunc("/", cp.handleIndex)
	if cp.App != nil {
		cp.App.routes(mux, cp.applyCORS)
	}

	cp.mu.Lock()
	defer cp.mu.Unlock()
	if cp.server != nil {
		return errors.New("controlplane: already started")
	}
	cp.server = &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", cp.port),
		Handler:           cp.logMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0, // streaming-friendly
	}
	ln, err := net.Listen("tcp", cp.server.Addr)
	if err != nil {
		return fmt.Errorf("controlplane: listen: %w", err)
	}
	cp.started = time.Now()
	srv := cp.server
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			cp.logger.Error("controlplane: serve", "err", err)
		}
	}()
	cp.logger.Info("controlplane: listening", "addr", srv.Addr)
	return nil
}

// Shutdown stops the control plane.
func (cp *ControlPlane) Shutdown(ctx context.Context) error {
	cp.mu.Lock()
	srv := cp.server
	cp.server = nil
	cp.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (cp *ControlPlane) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		next.ServeHTTP(rec, r)
		cp.logger.Info("http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.code,
			"dur_ms", float64(time.Since(start).Microseconds())/1000.0,
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (cp *ControlPlane) handleJSONStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cp.state.mu.RLock()
	resp := jsonStatsResponse{
		Status: jsonStatsStatus{
			WalletBalance:   cp.state.WalletBalance,
			TONLastSyncedAt: cp.state.TONLastSyncedAt,
			Enabled:         cp.state.Enabled,
			GitCommit:       cp.state.GitCommit,
		},
		LocalConf: jsonStatsLocalConf{
			RootAddress:      cp.state.RootAddress,
			OwnerAddress:     cp.state.OwnerAddress,
			CheckImageHashes: cp.state.CheckImageHashes,
		},
		ProxyConnections: append(make([]ProxyConnectionEntry, 0, len(cp.state.ProxyConnections)), cp.state.ProxyConnections...),
		Proxies:          append(make([]ProxyStatEntry, 0, len(cp.state.Proxies)), cp.state.Proxies...),
	}
	cp.state.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		cp.logger.Error("jsonstats encode", "err", err)
	}
}

// requestHandler runs the named verb against the proxy SC specified in
// ?proxy=<addr>. The query param is the cocoon_client SC address (matches
// upstream's runner-api.ts contract: the browser passes the proxy SC addr).
//
// The handler builds the appropriate cocoon_client external message body and
// broadcasts it via the configured cocoon-wallet broadcaster.
func (cp *ControlPlane) requestHandler(verb string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		proxyAddr := strings.TrimSpace(r.URL.Query().Get("proxy"))
		if proxyAddr == "" {
			fmt.Fprint(w, wrapShortAnswer("failed: missing proxy parameter"))
			return
		}

		clientSC, err := address.ParseAddr(proxyAddr)
		if err != nil {
			fmt.Fprint(w, wrapShortAnswer("failed: invalid proxy address"))
			return
		}
		broadcaster := cp.engine.Broadcaster()
		walletAddr := cp.engine.WalletAddr()
		if broadcaster == nil || walletAddr == nil {
			fmt.Fprint(w, wrapShortAnswer("failed: client not initialized"))
			return
		}

		body, err := cp.buildRequestBody(verb, walletAddr, r)
		if err != nil {
			fmt.Fprintln(w, wrapShortAnswer("failed: "+err.Error()))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		bocHash, err := broadcaster.BroadcastExternal(ctx, clientSC, body)
		if err != nil {
			cp.logger.Warn("/request/"+verb+" broadcast failed", "err", err, "client_sc", proxyAddr)
			fmt.Fprint(w, wrapShortAnswer("failed: "+err.Error()))
			return
		}
		cp.logger.Info("/request/"+verb+" broadcast", "client_sc", proxyAddr, "boc", bocHash)
		fmt.Fprint(w, wrapShortAnswer("Request sent"))
	}
}

// buildRequestBody constructs the cocoon_client SC body for the given verb,
// using the cocoon-wallet as excessesTo recipient.
func (cp *ControlPlane) buildRequestBody(verb string, excesses *address.Address, r *http.Request) (*cell.Cell, error) {
	switch verb {
	case "close":
		return client.BuildOwnerRequestRefund(excesses)
	case "withdraw":
		return client.BuildOwnerWithdraw(excesses)
	case "topup":
		// Allow optional ?amount=<nano>; default 1 TON if absent.
		amount := big.NewInt(1_000_000_000)
		if a := r.URL.Query().Get("amount"); a != "" {
			n, ok := new(big.Int).SetString(a, 10)
			if !ok || n.Sign() < 0 {
				return nil, fmt.Errorf("invalid amount %q", a)
			}
			amount = n
		}
		return client.BuildExtTopUp(amount, excesses)
	}
	return nil, fmt.Errorf("unknown verb %q", verb)
}

func (cp *ControlPlane) handleStats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html><html><body>
<h1>gocoon-runner</h1>
<p>started: %s</p>
<p><a href="/jsonstats">/jsonstats</a></p>
</body></html>`, cp.started.Format(time.RFC3339))
}

func (cp *ControlPlane) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintln(w, "gocoon-runner: see /jsonstats and /stats")
}

// wrapShortAnswer reproduces upstream's wrap_short_answer_to_http output.
func wrapShortAnswer(text string) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n")
	b.WriteString("<html><body>\n")
	b.WriteString(text)
	b.WriteString("<br/>\n")
	b.WriteString("<a href=\"/stats\">return to stats</a>\n")
	b.WriteString("</html></body>\n")
	return b.String()
}
