package main

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"

	cocoonwallet "github.com/TONresistor/gocoon/pkg/contracts/wallet"
	"github.com/TONresistor/gocoon/pkg/resources"
)

const defaultUIAddr = "127.0.0.1:17770"

//go:embed assets/*
var uiAssets embed.FS

func cmdUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	addr := fs.String("addr", defaultUIAddr, "UI listen address")
	dir := fs.String("dir", defaultUIDataDir(), "directory for wallet/config data")
	runnerPath := fs.String("runner", "", "path to gocoon-runner binary")
	open := fs.Bool("open", true, "open the UI in a browser")
	window := fs.Bool("window", false, "open the UI in a native desktop window")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("ui: unexpected argument %q", fs.Arg(0))
	}
	absDir, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	ui := newUIServer(absDir, *runnerPath, defaultRunnerURL)
	mux := http.NewServeMux()
	ui.routes(mux)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("ui: listen: %w", err)
	}
	url := "http://" + ln.Addr().String()
	fmt.Println("gocoon UI:", url)
	fmt.Println("data dir:", absDir)
	if *window {
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintln(os.Stderr, "ui: serve:", err)
			}
		}()
		err := runDesktopWindow(url)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		return err
	}
	if *open {
		_ = openBrowser(url)
	}
	return srv.Serve(ln)
}

func printUIUsage(out *os.File) {
	fmt.Fprintln(out, "gocoon ui : local wallet and chat UI")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  gocoon ui --dir ./gocoon-data [--runner ./gocoon-runner] [--addr 127.0.0.1:17770]")
	fmt.Fprintln(out, "  gocoon ui --window --dir ./gocoon-data")
}

func defaultUIDataDir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "Cocoon")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cocoon")
	}
	return "./gocoon-data"
}

type uiServer struct {
	dataDir    string
	walletPath string
	configPath string
	tonPath    string
	runnerPath string
	runnerURL  string

	mu     sync.Mutex
	runner *exec.Cmd
	logs   []string
}

func newUIServer(dataDir, runnerPath, runnerURL string) *uiServer {
	return &uiServer{
		dataDir:    dataDir,
		walletPath: filepath.Join(dataDir, "wallet.json"),
		configPath: filepath.Join(dataDir, "client-config.json"),
		tonPath:    filepath.Join(dataDir, "ton-config.json"),
		runnerPath: runnerPath,
		runnerURL:  strings.TrimRight(runnerURL, "/"),
	}
}

func (ui *uiServer) routes(mux *http.ServeMux) {
	mux.HandleFunc("/", ui.handleIndex)
	mux.Handle("/assets/", http.FileServer(http.FS(uiAssets)))
	mux.HandleFunc("/api/state", ui.handleState)
	mux.HandleFunc("/api/init", ui.handleInit)
	mux.HandleFunc("/api/wallet/backup", ui.handleWalletBackup)
	mux.HandleFunc("/api/wallet/qr.png", ui.handleWalletQR)
	mux.HandleFunc("/api/runner/start", ui.handleRunnerStart)
	mux.HandleFunc("/api/runner/stop", ui.handleRunnerStop)
	mux.HandleFunc("/api/models", ui.handleModels)
	mux.HandleFunc("/api/chat", ui.handleChat)
	mux.HandleFunc("/api/logs", ui.handleLogs)
}

type uiState struct {
	DataDir         string            `json:"data_dir"`
	WalletPath      string            `json:"wallet_path"`
	ConfigPath      string            `json:"config_path"`
	RunnerStatePath string            `json:"runner_state_path"`
	RunnerURL       string            `json:"runner_url"`
	Wallet          *walletInfoOutput `json:"wallet,omitempty"`
	Backup          *uiWalletBackup   `json:"backup,omitempty"`
	HasWallet       bool              `json:"has_wallet"`
	HasConfig       bool              `json:"has_config"`
	HasRunnerState  bool              `json:"has_runner_state"`
	Runner          uiRunnerState     `json:"runner"`
	Models          []uiModel         `json:"models,omitempty"`
	ModelsErr       string            `json:"models_error,omitempty"`
	StateErr        string            `json:"state_error,omitempty"`
}

type uiRunnerState struct {
	Running bool            `json:"running"`
	Managed bool            `json:"managed"`
	Error   string          `json:"error,omitempty"`
	Stats   json.RawMessage `json:"stats,omitempty"`
}

type uiModel struct {
	ID      string `json:"id"`
	Workers int    `json:"workers"`
}

type uiWalletBackup struct {
	WalletPath        string   `json:"wallet_path"`
	OwnerMnemonic     []string `json:"owner_mnemonic"`
	OwnerMnemonicText string   `json:"owner_mnemonic_text"`
	NodeSecretBase64  string   `json:"node_secret_base64"`
	OwnerAddress      string   `json:"owner_address"`
	FundAddress       string   `json:"fund_address"`
	BackupJSON        string   `json:"backup_json"`
}

type uiChatResponse struct {
	Content  string          `json:"content"`
	Thinking []string        `json:"thinking,omitempty"`
	Usage    *uiChatUsage    `json:"usage,omitempty"`
	Spend    *uiChatSpend    `json:"spend,omitempty"`
	Raw      json.RawMessage `json:"raw"`
}

type uiChatUsage struct {
	PromptTokens       int64  `json:"prompt_tokens,omitempty"`
	CachedTokens       int64  `json:"cached_tokens,omitempty"`
	CompletionTokens   int64  `json:"completion_tokens,omitempty"`
	ReasoningTokens    int64  `json:"reasoning_tokens,omitempty"`
	TotalTokens        int64  `json:"total_tokens,omitempty"`
	PromptCostNano     string `json:"prompt_cost_nano,omitempty"`
	PromptCostTON      string `json:"prompt_cost_ton,omitempty"`
	CompletionCostNano string `json:"completion_cost_nano,omitempty"`
	CompletionCostTON  string `json:"completion_cost_ton,omitempty"`
	TotalCostNano      string `json:"total_cost_nano,omitempty"`
	TotalCostTON       string `json:"total_cost_ton,omitempty"`
	HasCost            bool   `json:"has_cost,omitempty"`
}

type uiChatSpend struct {
	Label              string `json:"label,omitempty"`
	Source             string `json:"source,omitempty"`
	TotalNano          string `json:"total_nano,omitempty"`
	TotalTON           string `json:"total_ton,omitempty"`
	Tokens             int64  `json:"tokens,omitempty"`
	TokensChargedDelta int64  `json:"tokens_charged_delta,omitempty"`
	TokensPayedDelta   int64  `json:"tokens_payed_delta,omitempty"`
}

func (ui *uiServer) state(ctx context.Context) uiState {
	runnerStatePath := filepath.Join(ui.dataDir, "runner-state.bolt")
	st := uiState{
		DataDir:         ui.dataDir,
		WalletPath:      ui.walletPath,
		ConfigPath:      ui.configPath,
		RunnerStatePath: runnerStatePath,
		RunnerURL:       ui.runnerURL,
		HasWallet:       fileExists(ui.walletPath),
		HasConfig:       fileExists(ui.configPath),
		HasRunnerState:  nonEmptyFileExists(runnerStatePath),
	}
	ui.mu.Lock()
	st.Runner.Managed = ui.runner != nil && ui.runner.Process != nil && ui.runner.ProcessState == nil
	ui.mu.Unlock()

	if st.HasWallet {
		info, err := loadWalletInfo(ui.walletPath)
		if err != nil {
			st.StateErr = err.Error()
		} else {
			out := &walletInfoOutput{
				WalletPath:             ui.walletPath,
				OwnerAddress:           info.OwnerAddress,
				FundAddress:            info.NodeAddress,
				RecommendedFundingNano: strconv.FormatUint(recommendedCocoonFundingNano, 10),
				RecommendedFundingTON:  formatNanoTON(new(big.Int).SetUint64(recommendedCocoonFundingNano)),
			}
			if st.HasConfig {
				ctx2, cancel := context.WithTimeout(ctx, 8*time.Second)
				status, err := fetchFundingStatus(ctx2, ui.tonPath, info.NodeAddress)
				cancel()
				if err == nil {
					out.TONConfigPath = ui.tonPath
					out.BalanceNano = status.BalanceNano.String()
					out.BalanceTON = formatNanoTON(status.BalanceNano)
					funded := status.BalanceNano.Cmp(new(big.Int).SetUint64(recommendedCocoonFundingNano)) >= 0
					out.Funded = &funded
					if status.MinClientStakeNano != nil {
						out.OnChainMinClientStakeNano = status.MinClientStakeNano.String()
						out.OnChainMinClientStakeTON = formatNanoTON(status.MinClientStakeNano)
					}
				} else if st.StateErr == "" {
					st.StateErr = err.Error()
				}
			}
			st.Wallet = out
		}
	}

	stats, err := ui.fetchRunner("/jsonstats")
	if err == nil {
		st.Runner.Running = true
		st.Runner.Stats = stats
		models, modelsErr := ui.fetchModels()
		if modelsErr == nil {
			st.Models = models
		} else {
			st.ModelsErr = modelsErr.Error()
		}
	} else {
		st.Runner.Error = err.Error()
	}
	return st
}

func (ui *uiServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, uiHTML)
}

func (ui *uiServer) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, ui.state(r.Context()))
}

func (ui *uiServer) handleInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if fileExists(ui.walletPath) && fileExists(ui.configPath) {
		writeJSON(w, ui.state(r.Context()))
		return
	}
	backup, err := createUIFiles(ui.dataDir, false, 10000, 10001)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	st := ui.state(r.Context())
	st.Backup = backup
	writeJSON(w, st)
}

func (ui *uiServer) handleWalletBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !fileExists(ui.walletPath) {
		writeJSONError(w, http.StatusBadRequest, "create a wallet first")
		return
	}
	backup, err := readUIWalletBackup(ui.walletPath)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"backup": backup})
}

func (ui *uiServer) handleWalletQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !fileExists(ui.walletPath) {
		http.Error(w, "create a wallet first", http.StatusBadRequest)
		return
	}
	info, err := loadWalletInfo(ui.walletPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	payload := "ton://transfer/" + url.PathEscape(info.NodeAddress)
	png, err := qrcode.Encode(payload, qrcode.Medium, 288)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (ui *uiServer) handleRunnerStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !fileExists(ui.configPath) {
		writeJSONError(w, http.StatusBadRequest, "create a wallet first")
		return
	}
	if _, err := ui.fetchRunner("/jsonstats"); err == nil {
		writeJSON(w, ui.state(r.Context()))
		return
	}
	ui.mu.Lock()
	if ui.runner != nil && ui.runner.Process != nil && ui.runner.ProcessState == nil {
		ui.mu.Unlock()
		writeJSON(w, ui.state(r.Context()))
		return
	}
	ui.mu.Unlock()

	path, err := resolveRunnerPath(ui.runnerPath)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	cmd := exec.Command(path, "--config", ui.configPath, "-v3")
	cmd.Env = append(os.Environ(), "GOCOON_DATA_DIR="+ui.dataDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	ui.mu.Lock()
	ui.runner = cmd
	ui.appendLogLocked("runner started: " + path)
	ui.mu.Unlock()
	go ui.pipeLogs("out", stdout)
	go ui.pipeLogs("err", stderr)
	go ui.waitRunner(cmd)

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := ui.fetchRunner("/jsonstats"); err == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	writeJSON(w, ui.state(r.Context()))
}

func (ui *uiServer) handleRunnerStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ui.mu.Lock()
	cmd := ui.runner
	ui.runner = nil
	ui.mu.Unlock()
	if cmd != nil && cmd.Process != nil && cmd.ProcessState == nil {
		_ = cmd.Process.Kill()
	}
	writeJSON(w, ui.state(r.Context()))
}

func (ui *uiServer) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	models, err := ui.fetchModels()
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{"models": models})
}

func (ui *uiServer) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Model    string              `json:"model"`
		Prompt   string              `json:"prompt"`
		Messages []map[string]string `json:"messages"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*1024*1024)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Model == "" {
		req.Model = "default"
	}
	if len(req.Messages) == 0 && strings.TrimSpace(req.Prompt) != "" {
		req.Messages = []map[string]string{{"role": "user", "content": strings.TrimSpace(req.Prompt)}}
	}
	if len(req.Messages) == 0 {
		writeJSONError(w, http.StatusBadRequest, "message is required")
		return
	}
	body, err := json.Marshal(map[string]any{
		"model":    req.Model,
		"stream":   false,
		"messages": req.Messages,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	beforeStats, _ := ui.fetchRunner("/jsonstats")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Post(ui.runnerURL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSONError(w, http.StatusBadGateway, strings.TrimSpace(string(out)))
		return
	}
	chatResp := parseUIChatResponse(out)
	if afterStats, err := ui.fetchRunner("/jsonstats"); err == nil {
		applyRunnerSpendDelta(chatResp, beforeStats, afterStats)
	}
	writeJSON(w, chatResp)
}

func (ui *uiServer) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ui.mu.Lock()
	logs := append([]string(nil), ui.logs...)
	ui.mu.Unlock()
	writeJSON(w, map[string]any{"logs": logs})
}

func (ui *uiServer) waitRunner(cmd *exec.Cmd) {
	err := cmd.Wait()
	ui.mu.Lock()
	defer ui.mu.Unlock()
	if err != nil {
		ui.appendLogLocked("runner exited: " + err.Error())
	} else {
		ui.appendLogLocked("runner exited")
	}
	if ui.runner == cmd {
		ui.runner = nil
	}
}

func (ui *uiServer) pipeLogs(prefix string, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		ui.mu.Lock()
		ui.appendLogLocked(prefix + ": " + sc.Text())
		ui.mu.Unlock()
	}
}

func (ui *uiServer) appendLogLocked(line string) {
	ui.logs = append(ui.logs, time.Now().Format("15:04:05")+" "+line)
	if len(ui.logs) > 400 {
		ui.logs = ui.logs[len(ui.logs)-400:]
	}
}

func (ui *uiServer) fetchRunner(path string) ([]byte, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(ui.runnerURL + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (ui *uiServer) fetchModels() ([]uiModel, error) {
	body, err := ui.fetchRunner("/v1/models")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			ID      string `json:"id"`
			Workers []any  `json:"workers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	out := make([]uiModel, 0, len(resp.Data))
	for _, model := range resp.Data {
		out = append(out, uiModel{ID: model.ID, Workers: len(model.Workers)})
	}
	return out, nil
}

func createUIFiles(dir string, force bool, httpPort, rpcPort int) (*uiWalletBackup, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("init: create dir: %w", err)
	}
	walletPath := filepath.Join(dir, "wallet.json")
	configPath := filepath.Join(dir, "client-config.json")
	tonConfigPath := filepath.Join(dir, "ton-config.json")
	for _, path := range []string{walletPath, configPath, tonConfigPath} {
		if err := preflightOutputFile(path, force); err != nil {
			return nil, err
		}
	}
	code, _, err := cocoonwallet.LoadDefaultCode()
	if err != nil {
		return nil, err
	}
	generated, err := cocoonwallet.Generate(cocoonwallet.GenerateOptions{Code: code})
	if err != nil {
		return nil, err
	}
	walletJSON, err := json.MarshalIndent(generated, "", "  ")
	if err != nil {
		return nil, err
	}
	walletJSON = append(walletJSON, '\n')
	if err := writeOutputFile(walletPath, walletJSON, 0o600, force); err != nil {
		return nil, err
	}
	if err := writeOutputFile(tonConfigPath, resources.TONConfigJSON, 0o600, force); err != nil {
		return nil, err
	}
	absTonConfig, err := filepath.Abs(tonConfigPath)
	if err != nil {
		return nil, err
	}
	cfg := runnerConfigJSON{
		IsTest:            false,
		IsTestnet:         false,
		HTTPPort:          httpPort,
		RPCPort:           rpcPort,
		ProxyConnections:  1,
		TonConfigFilename: absTonConfig,
		OwnerAddress:      generated.OwnerAddress,
		RootContractAddr:  defaultMainnetRoot,
		NodeWalletKey:     generated.NodeSecretBase64,
		MaxCoefficient:    0,
		MaxTokens:         0,
	}
	configJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	configJSON = append(configJSON, '\n')
	if err := writeOutputFile(configPath, configJSON, 0o600, force); err != nil {
		return nil, err
	}
	return newUIWalletBackup(walletPath, generated, walletJSON), nil
}

func readUIWalletBackup(walletPath string) (*uiWalletBackup, error) {
	raw, err := os.ReadFile(walletPath)
	if err != nil {
		return nil, fmt.Errorf("backup: read wallet: %w", err)
	}
	var generated cocoonwallet.Generated
	if err := json.Unmarshal(raw, &generated); err != nil {
		return nil, fmt.Errorf("backup: parse wallet: %w", err)
	}
	if len(generated.OwnerMnemonic) == 0 || generated.NodeSecretBase64 == "" {
		return nil, errors.New("backup: wallet file does not contain recovery data")
	}
	var pretty bytes.Buffer
	backupJSON := raw
	if err := json.Indent(&pretty, raw, "", "  "); err == nil {
		pretty.WriteByte('\n')
		backupJSON = pretty.Bytes()
	}
	return newUIWalletBackup(walletPath, &generated, backupJSON), nil
}

func newUIWalletBackup(walletPath string, generated *cocoonwallet.Generated, walletJSON []byte) *uiWalletBackup {
	return &uiWalletBackup{
		WalletPath:        walletPath,
		OwnerMnemonic:     append([]string(nil), generated.OwnerMnemonic...),
		OwnerMnemonicText: strings.Join(generated.OwnerMnemonic, " "),
		NodeSecretBase64:  generated.NodeSecretBase64,
		OwnerAddress:      generated.OwnerAddress,
		FundAddress:       generated.NodeAddress,
		BackupJSON:        string(walletJSON),
	}
}

func parseUIChatResponse(data []byte) *uiChatResponse {
	content, usage := parseOpenAIChatPayload(data)
	visible, thinking := splitThinkBlocks(content)
	resp := &uiChatResponse{
		Content:  visible,
		Thinking: thinking,
		Usage:    usage,
		Raw:      safeRawJSON(data),
	}
	resp.Spend = buildChatSpend(usage, nil)
	return resp
}

func safeRawJSON(data []byte) json.RawMessage {
	if json.Valid(data) {
		return append(json.RawMessage(nil), data...)
	}
	quoted, err := json.Marshal(string(data))
	if err != nil {
		return json.RawMessage(`""`)
	}
	return quoted
}

func parseOpenAIChatPayload(data []byte) (string, *uiChatUsage) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&resp); err != nil {
		return string(data), nil
	}
	content := string(data)
	if len(resp.Choices) > 0 {
		content = resp.Choices[0].Message.Content
	}
	return content, parseChatUsage(resp.Usage)
}

func splitThinkBlocks(text string) (string, []string) {
	var visible strings.Builder
	thinking := make([]string, 0, 1)
	cursor := 0
	for {
		openRel := indexFold(text[cursor:], "<think>")
		if openRel < 0 {
			visible.WriteString(text[cursor:])
			break
		}
		open := cursor + openRel
		visible.WriteString(text[cursor:open])
		thinkStart := open + len("<think>")
		closeRel := indexFold(text[thinkStart:], "</think>")
		if closeRel < 0 {
			if thought := cleanChatText(text[thinkStart:]); thought != "" {
				thinking = append(thinking, thought)
			}
			break
		}
		closeAt := thinkStart + closeRel
		if thought := cleanChatText(text[thinkStart:closeAt]); thought != "" {
			thinking = append(thinking, thought)
		}
		cursor = closeAt + len("</think>")
	}
	return cleanChatText(visible.String()), thinking
}

func indexFold(s, substr string) int {
	return strings.Index(strings.ToLower(s), strings.ToLower(substr))
}

func cleanChatText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimSpace(text)
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	return text
}

func parseChatUsage(m map[string]any) *uiChatUsage {
	if len(m) == 0 {
		return nil
	}
	usage := &uiChatUsage{
		PromptTokens:     int64FromJSONPath(m, "prompt_tokens"),
		CachedTokens:     int64FromJSONPath(m, "prompt_tokens_details", "cached_tokens"),
		CompletionTokens: int64FromJSONPath(m, "completion_tokens"),
		ReasoningTokens:  int64FromJSONPath(m, "completion_tokens_details", "reasoning_tokens"),
		TotalTokens:      int64FromJSONPath(m, "total_tokens"),
	}
	if usage.CachedTokens == 0 {
		usage.CachedTokens = int64FromJSONPath(m, "cached_tokens")
	}
	if usage.ReasoningTokens == 0 {
		usage.ReasoningTokens = int64FromJSONPath(m, "reasoning_tokens")
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	setUsageCost := func(nano *big.Int, nanoField *string, tonField *string) {
		if nano == nil {
			return
		}
		*nanoField = nano.String()
		*tonField = formatNanoTON(nano)
		usage.HasCost = true
	}
	setUsageCost(bigIntFromJSONPath(m, "prompt_total_cost"), &usage.PromptCostNano, &usage.PromptCostTON)
	setUsageCost(bigIntFromJSONPath(m, "completion_total_cost"), &usage.CompletionCostNano, &usage.CompletionCostTON)
	setUsageCost(bigIntFromJSONPath(m, "total_cost"), &usage.TotalCostNano, &usage.TotalCostTON)
	if usage.TotalTokens == 0 && !usage.HasCost {
		return nil
	}
	return usage
}

func int64FromJSONPath(m map[string]any, path ...string) int64 {
	v, ok := jsonPathValue(m, path...)
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(n.String(), 64); err == nil {
			return int64(f)
		}
	case float64:
		return int64(n)
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i
		}
	}
	return 0
}

func bigIntFromJSONPath(m map[string]any, path ...string) *big.Int {
	v, ok := jsonPathValue(m, path...)
	if !ok {
		return nil
	}
	switch n := v.(type) {
	case json.Number:
		return bigIntFromNumberString(n.String())
	case float64:
		return big.NewInt(int64(n))
	case string:
		return bigIntFromNumberString(strings.TrimSpace(n))
	}
	return nil
}

func jsonPathValue(m map[string]any, path ...string) (any, bool) {
	var cur any = m
	for _, part := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func bigIntFromNumberString(s string) *big.Int {
	if s == "" {
		return nil
	}
	if i, ok := new(big.Int).SetString(s, 10); ok {
		return i
	}
	if r, ok := new(big.Rat).SetString(s); ok {
		return new(big.Int).Quo(r.Num(), r.Denom())
	}
	return nil
}

type runnerSpendTotals struct {
	TokensCharged int64
	TokensPayed   int64
}

func parseRunnerSpendTotals(data []byte) (runnerSpendTotals, bool) {
	if len(data) == 0 {
		return runnerSpendTotals{}, false
	}
	var stats struct {
		Proxies []struct {
			TokensCharged int64 `json:"tokens_charged"`
			TokensPayed   int64 `json:"tokens_payed"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(data, &stats); err != nil {
		return runnerSpendTotals{}, false
	}
	var out runnerSpendTotals
	for _, proxy := range stats.Proxies {
		out.TokensCharged += proxy.TokensCharged
		out.TokensPayed += proxy.TokensPayed
	}
	return out, true
}

func applyRunnerSpendDelta(resp *uiChatResponse, beforeRaw, afterRaw []byte) {
	before, okBefore := parseRunnerSpendTotals(beforeRaw)
	after, okAfter := parseRunnerSpendTotals(afterRaw)
	if !okBefore || !okAfter {
		return
	}
	delta := &runnerSpendTotals{
		TokensCharged: after.TokensCharged - before.TokensCharged,
		TokensPayed:   after.TokensPayed - before.TokensPayed,
	}
	if delta.TokensCharged < 0 {
		delta.TokensCharged = 0
	}
	if delta.TokensPayed < 0 {
		delta.TokensPayed = 0
	}
	resp.Spend = buildChatSpend(resp.Usage, delta)
}

func buildChatSpend(usage *uiChatUsage, delta *runnerSpendTotals) *uiChatSpend {
	if usage == nil && (delta == nil || (delta.TokensCharged == 0 && delta.TokensPayed == 0)) {
		return nil
	}
	spend := &uiChatSpend{}
	if usage != nil {
		spend.Tokens = usage.TotalTokens
		if usage.TotalCostTON != "" {
			spend.Source = "usage"
			spend.TotalNano = usage.TotalCostNano
			spend.TotalTON = usage.TotalCostTON
			spend.Label = usage.TotalCostTON + " TON"
			if usage.TotalTokens > 0 {
				spend.Label += " / " + strconv.FormatInt(usage.TotalTokens, 10) + " tokens"
			}
		} else if usage.TotalTokens > 0 {
			spend.Source = "usage"
			spend.Label = strconv.FormatInt(usage.TotalTokens, 10) + " tokens"
		}
	}
	if delta != nil {
		spend.TokensChargedDelta = delta.TokensCharged
		spend.TokensPayedDelta = delta.TokensPayed
		if spend.Label == "" && delta.TokensCharged > 0 {
			spend.Source = "runner_stats"
			spend.Label = strconv.FormatInt(delta.TokensCharged, 10) + " charged tokens"
		}
	}
	if spend.Label == "" && spend.Tokens == 0 && spend.TokensChargedDelta == 0 && spend.TokensPayedDelta == 0 {
		return nil
	}
	return spend
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func nonEmptyFileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

const uiHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Cocoon</title>
<link rel="icon" type="image/png" href="/assets/cocoon-favicon-32x32.png">
<style>
:root {
  color-scheme: dark;
  --bg: #010715;
  --bg-deep: #020e33;
  --sidebar: rgba(3, 9, 30, .92);
  --surface: rgba(10, 24, 58, .74);
  --surface-2: rgba(31, 89, 198, .26);
  --surface-3: rgba(48, 136, 255, .18);
  --line: #103d60;
  --line-soft: rgba(16, 61, 96, .68);
  --text: #ffffff;
  --muted: #bfc8df;
  --subtle: #767e90;
  --accent: #68beff;
  --accent-2: #d277ff;
  --accent-hover: #8ed0ff;
  --accent-soft: rgba(104, 190, 255, .15);
  --warn: #f1c66a;
  --warn-soft: rgba(242, 201, 76, .13);
  --danger: #ff6b6b;
  --danger-soft: rgba(255, 107, 107, .12);
  --ok: #61d8ff;
  --shadow: rgba(0, 0, 0, .32);
  --brand-gradient: linear-gradient(290deg, #d235ff 0%, #a062ff 30%, #3088ff 66%, #61d8ff 100%);
}
* { box-sizing: border-box; }
body {
  margin: 0;
  min-height: 100vh;
  overflow: hidden;
  background: #111c40 linear-gradient(180deg, #020e33 0%, var(--bg) 25%, var(--bg) 84%, #111c40 100%);
  color: var(--text);
  font-family: ProductSans, Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}
button, input, select, textarea { font: inherit; }
button {
  border: 1px solid var(--line);
  background: var(--surface-2);
  color: var(--text);
  border-radius: 8px;
  min-height: 38px;
  padding: 0 13px;
  cursor: pointer;
}
button:hover:not(:disabled) { background: var(--surface-3); }
button:disabled { opacity: .45; cursor: not-allowed; }
button.primary {
  background:
    linear-gradient(180deg, rgba(31, 89, 198, .26), rgba(31, 89, 198, .26)) padding-box,
    linear-gradient(180deg, rgba(78, 173, 252, .61) 0%, #1F59C6 53%, #20397F 100%) border-box;
  border-color: transparent;
  color: #fff;
  font-weight: 700;
}
button.primary:hover:not(:disabled) {
  background:
    linear-gradient(180deg, rgba(48, 136, 255, .34), rgba(31, 89, 198, .30)) padding-box,
    linear-gradient(180deg, #68beff 0%, #3088ff 53%, #20397F 100%) border-box;
}
button.ghost { background: transparent; }
button.icon {
  width: 38px;
  padding: 0;
  display: inline-grid;
  place-items: center;
}
select, textarea {
  width: 100%;
  border: 1px solid var(--line);
  background: rgba(1, 7, 21, .72);
  color: var(--text);
  border-radius: 8px;
  outline: none;
}
select { min-height: 38px; padding: 0 10px; }
textarea {
  min-height: 58px;
  max-height: 180px;
  resize: vertical;
  padding: 12px 78px 12px 13px;
  line-height: 1.45;
}
h1, h2, h3, p { margin: 0; }
h1 { font-size: 18px; line-height: 1.25; }
h2 {
  color: var(--muted);
  font-size: 12px;
  font-weight: 700;
  letter-spacing: .08em;
  margin-bottom: 10px;
  text-transform: uppercase;
}
.app {
  display: grid;
  grid-template-columns: 364px minmax(0, 1fr);
  height: 100vh;
}
html.desktop .app {
  height: 100vh;
}
.window-shell {
  height: 100vh;
  display: grid;
  grid-template-rows: minmax(0, 1fr);
}
html.desktop .window-shell {
  grid-template-rows: minmax(0, 1fr);
}
.titlebar {
  display: none;
  position: fixed;
  top: 10px;
  right: 10px;
  z-index: 40;
  height: 34px;
  user-select: none;
  pointer-events: none;
}
html.desktop .titlebar { display: flex; }
.titlebar-drag {
  display: none;
}
.titlebar-logo {
  display: none;
}
.titlebar-title {
  display: none;
}
.window-controls {
  display: flex;
  overflow: hidden;
  align-items: stretch;
  pointer-events: auto;
  border: 1px solid rgba(104, 190, 255, .22);
  border-radius: 8px;
  background: rgba(1, 7, 21, .72);
  backdrop-filter: blur(18px);
}
.window-control {
  width: 40px;
  min-height: 32px;
  border: 0;
  border-radius: 0;
  background: transparent;
  color: var(--muted);
  padding: 0;
}
.window-control:hover:not(:disabled) { background: rgba(255, 255, 255, .08); }
.window-control.close:hover:not(:disabled) { background: #c42b1c; color: #fff; }
.window-control svg {
  width: 14px;
  height: 14px;
  pointer-events: none;
}
.sidebar {
  display: grid;
  grid-template-rows: auto minmax(0, 1fr) auto;
  gap: 14px;
  min-width: 0;
  padding: 16px;
  background: var(--sidebar);
  border-right: 1px solid var(--line-soft);
}
.brand {
  display: flex;
  align-items: center;
  min-height: 42px;
}
.brand-mark {
  display: none;
}
.brand h1 {
  background: var(--brand-gradient);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
}
.brand small {
  display: block;
  margin-top: 2px;
  color: var(--muted);
  font-size: 12px;
}
.side-scroll {
  min-height: 0;
  overflow: auto;
  display: grid;
  align-content: start;
  gap: 12px;
}
.card {
  border: 1px solid var(--line);
  background: var(--surface);
  border-radius: 8px;
  padding: 12px;
  box-shadow: 0 12px 28px var(--shadow);
}
.stack { display: grid; gap: 10px; }
.row { display: flex; align-items: center; gap: 8px; }
.row.between { justify-content: space-between; }
.row.actions { flex-wrap: wrap; }
.row.actions > button { flex: 1 1 96px; }
.row > * { min-width: 0; }
.label {
  margin-bottom: 5px;
  color: var(--muted);
  font-size: 12px;
}
.address {
  border: 1px solid var(--line);
  background: rgba(1, 7, 21, .70);
  border-radius: 8px;
  padding: 10px;
  color: #d7dbdf;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.45;
  word-break: break-all;
}
.qr-wrap {
  display: none;
  grid-template-columns: 112px minmax(0, 1fr);
  gap: 12px;
  align-items: center;
  border: 1px solid var(--line);
  background: rgba(1, 7, 21, .54);
  border-radius: 8px;
  padding: 10px;
}
.qr-wrap.show { display: grid; }
.qr-box {
  width: 112px;
  height: 112px;
  border-radius: 8px;
  background: #fff;
  padding: 6px;
}
.qr-box img {
  display: block;
  width: 100%;
  height: 100%;
}
.qr-text {
  display: grid;
  gap: 6px;
  color: var(--muted);
  font-size: 13px;
  line-height: 1.4;
}
.qr-text b { color: var(--text); }
.metric {
  display: grid;
  grid-template-columns: 1fr auto;
  align-items: center;
  gap: 8px;
  color: var(--muted);
  font-size: 13px;
}
.metric strong { color: var(--text); font-size: 15px; }
.pill {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-height: 26px;
  padding: 0 9px;
  border: 1px solid var(--line);
  border-radius: 999px;
  color: var(--muted);
  background: rgba(255, 255, 255, .02);
  font-size: 12px;
  white-space: nowrap;
}
.pill.ok { color: var(--ok); border-color: rgba(91, 211, 139, .45); background: rgba(91, 211, 139, .08); }
.pill.warn { color: var(--warn); border-color: rgba(242, 201, 76, .45); background: var(--warn-soft); }
.pill.bad { color: var(--danger); border-color: rgba(255, 107, 107, .42); background: var(--danger-soft); }
.hint {
  border: 1px solid rgba(242, 201, 76, .32);
  background: var(--warn-soft);
  border-radius: 8px;
  padding: 10px;
  color: #f2df9d;
  font-size: 13px;
  line-height: 1.45;
}
.main {
  position: relative;
  min-width: 0;
  height: 100vh;
  display: grid;
  grid-template-rows: auto minmax(0, 1fr) auto;
  background:
    radial-gradient(circle at 78% 12%, rgba(210, 53, 255, .10), transparent 28%),
    radial-gradient(circle at 58% 0%, rgba(104, 190, 255, .14), transparent 32%),
    var(--bg);
  overflow: hidden;
}
html.desktop .main {
  height: 100vh;
}
.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  min-height: 64px;
  padding: 0 22px;
  border-bottom: 1px solid var(--line-soft);
}
html.desktop .topbar {
  padding-right: 150px;
}
.topbar-title {
  display: flex;
  align-items: center;
  gap: 10px;
}
.status-dot {
  width: 8px;
  height: 8px;
  border-radius: 99px;
  background: var(--subtle);
}
.status-dot.ok { background: var(--ok); }
.status-dot.warn { background: var(--warn); }
.top-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.chat {
  min-height: 0;
  overflow: auto;
  padding: 28px 24px 20px;
}
.conversation {
  width: min(840px, 100%);
  margin: 0 auto;
  display: grid;
  gap: 18px;
}
.empty {
  margin-top: 8vh;
  display: grid;
  gap: 18px;
  text-align: center;
}
.empty h2 {
  color: var(--text);
  font-size: 28px;
  letter-spacing: 0;
  text-transform: none;
  margin: 0;
}
.empty p {
  color: var(--muted);
  line-height: 1.55;
}
.steps {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}
.step {
  border: 1px solid var(--line);
  background: var(--surface);
  border-radius: 8px;
  padding: 12px;
  text-align: left;
}
.step b { display: block; margin-bottom: 5px; }
.step span { color: var(--muted); font-size: 13px; line-height: 1.4; }
.msg {
  display: grid;
  gap: 6px;
}
.msg.user { justify-items: end; }
.role {
  color: var(--muted);
  font-size: 12px;
}
.bubble {
  max-width: min(720px, 100%);
  border: 1px solid var(--line);
  background: var(--surface);
  border-radius: 8px;
  padding: 13px 15px;
  white-space: pre-wrap;
  line-height: 1.55;
  box-shadow: 0 10px 26px rgba(0,0,0,.18);
}
.user .bubble {
  background: rgba(48, 136, 255, .18);
  border-color: rgba(104, 190, 255, .34);
}
.thinking {
  width: min(720px, 100%);
  border-color: rgba(210, 119, 255, .28);
  background: rgba(210, 119, 255, .08);
  box-shadow: none;
}
.thinking summary {
  color: #dac4ff;
}
.thinking div {
  margin-top: 9px;
  color: var(--muted);
  font-size: 13px;
  line-height: 1.5;
  white-space: pre-wrap;
}
.message-meta {
  max-width: min(720px, 100%);
  color: var(--subtle);
  font-size: 12px;
}
.message-meta b {
  color: var(--accent);
  font-weight: 600;
}
.composer {
  border-top: 1px solid var(--line-soft);
  padding: 14px 24px 18px;
  background: rgba(1, 7, 21, .84);
}
.composer-inner {
  width: min(840px, 100%);
  margin: 0 auto;
  display: grid;
  gap: 9px;
  border: 1px solid rgba(104, 190, 255, .24);
  background: rgba(4, 16, 42, .82);
  border-radius: 8px;
  padding: 10px;
  box-shadow: 0 18px 50px rgba(0,0,0,.28);
}
.composer-top {
  display: flex;
  justify-content: space-between;
  gap: 10px;
  align-items: center;
}
.model-field {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.model-field .label {
  margin: 0;
  white-space: nowrap;
}
.model-field select {
  width: min(360px, 48vw);
  min-height: 34px;
}
.prompt-wrap {
  position: relative;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: rgba(1, 7, 21, .58);
}
.prompt-wrap textarea {
  border: 0;
  background: transparent;
  min-height: 66px;
}
.send {
  position: absolute;
  right: 10px;
  bottom: 10px;
  width: 58px;
  min-height: 34px;
  padding: 0;
  border-radius: 99px;
}
.error {
  color: var(--danger);
  font-size: 13px;
  min-height: 18px;
}
details {
  border: 1px solid var(--line);
  background: var(--surface);
  border-radius: 8px;
  padding: 10px 12px;
}
summary {
  cursor: pointer;
  color: var(--muted);
  font-size: 13px;
}
.logbox {
  margin-top: 10px;
  max-height: 180px;
  overflow: auto;
  border-radius: 8px;
  background: rgba(1, 7, 21, .72);
  padding: 10px;
  color: var(--muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
  line-height: 1.45;
  white-space: pre-wrap;
}
.modal {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: none;
  place-items: center;
  padding: 24px;
  background: rgba(0, 0, 0, .64);
}
.modal.show { display: grid; }
.modal-panel {
  width: min(720px, 100%);
  max-height: min(760px, 92vh);
  overflow: auto;
  border: 1px solid var(--line);
  background: #18191d;
  border-radius: 8px;
  box-shadow: 0 28px 80px rgba(0, 0, 0, .55);
}
.modal-head {
  display: flex;
  justify-content: space-between;
  gap: 14px;
  padding: 18px;
  border-bottom: 1px solid var(--line-soft);
}
.modal-body {
  display: grid;
  gap: 14px;
  padding: 18px;
}
.seed-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
}
.seed-word {
  display: grid;
  grid-template-columns: 28px minmax(0, 1fr);
  gap: 8px;
  align-items: center;
  min-height: 38px;
  border: 1px solid var(--line);
  background: #121316;
  border-radius: 8px;
  padding: 0 10px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
}
.seed-word span {
  color: var(--subtle);
  font-size: 11px;
}
.backup-json {
  min-height: 140px;
  padding: 10px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px;
}
@media (max-width: 900px) {
  body { overflow: auto; }
  .app { grid-template-columns: 1fr; height: auto; min-height: 100vh; }
  .sidebar { border-right: 0; border-bottom: 1px solid var(--line-soft); }
  .main { height: 72vh; }
  .steps { grid-template-columns: 1fr; }
  .seed-grid { grid-template-columns: 1fr; }
}

/* Desktop redesign */
:root {
  --app-bg: #08090c;
  --chrome: #111216;
  --panel: #15171c;
  --panel-2: #1b1e25;
  --panel-3: #20242c;
  --stroke: #2a2f38;
  --stroke-soft: #20242b;
  --copy: #f4f6fb;
  --copy-2: #c8ced8;
  --copy-3: #858c99;
  --blue: #65c7ff;
  --violet: #c47aff;
  --green: #69d58c;
  --yellow: #e8bd61;
  --red: #ff6b6b;
}
body {
  background: var(--app-bg);
  color: var(--copy);
}
button {
  min-height: 34px;
  border: 1px solid var(--stroke);
  border-radius: 7px;
  background: #1b1d23;
  color: var(--copy);
  padding: 0 11px;
}
button:hover:not(:disabled) {
  background: #232731;
  border-color: #3a414d;
}
button.primary {
  background: linear-gradient(180deg, #2b8ee8, #1967c2);
  border-color: #4aa8ff;
  font-weight: 650;
}
button.primary:hover:not(:disabled) {
  background: linear-gradient(180deg, #37a4ff, #2477d8);
}
button.primary:disabled {
  background: #151820;
  border-color: var(--stroke-soft);
  color: var(--copy-3);
}
#createWallet:disabled {
  display: none;
}
#startRunner:disabled,
#stopRunner:disabled {
  display: none;
}
button.ghost {
  background: transparent;
}
button.icon {
  width: 34px;
  min-height: 34px;
}
select, textarea {
  border-color: var(--stroke);
  background: #101217;
  color: var(--copy);
}
select {
  min-height: 34px;
  padding: 0 32px 0 10px;
}
textarea {
  min-height: 52px;
  padding: 13px 58px 13px 14px;
}
h1 { font-size: 16px; }
h2 {
  margin: 0;
  color: var(--copy-3);
  font-size: 11px;
  letter-spacing: .11em;
}
.window-shell {
  height: 100vh;
  display: grid;
  grid-template-rows: 40px minmax(0, 1fr);
  background: var(--app-bg);
}
.titlebar,
html.desktop .titlebar {
  position: static;
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  height: 40px;
  border-bottom: 1px solid var(--stroke-soft);
  background: #0d0e12;
  pointer-events: auto;
}
.titlebar-drag {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  padding: 0 12px;
}
.titlebar-logo {
  display: block;
  width: 18px;
  height: 18px;
}
.titlebar-title {
  display: block;
  color: var(--copy-2);
  font-size: 12px;
  font-weight: 650;
}
.window-controls {
  height: 40px;
  border: 0;
  border-radius: 0;
  background: transparent;
  backdrop-filter: none;
}
.window-control {
  width: 44px;
  min-height: 40px;
  color: var(--copy-3);
}
.app {
  height: calc(100vh - 40px);
  display: grid;
  grid-template-columns: 336px minmax(0, 1fr);
  background: var(--app-bg);
}
.sidebar {
  min-height: 0;
  padding: 12px;
  gap: 10px;
  border-right: 1px solid var(--stroke-soft);
  background: #0f1116;
}
.brand {
  min-height: 36px;
  display: grid;
  align-items: center;
}
.brand h1 {
  color: var(--copy);
  background: none;
  font-size: 17px;
  letter-spacing: 0;
}
.brand small {
  color: var(--copy-3);
  font-size: 11px;
}
.side-scroll {
  gap: 9px;
  padding-right: 2px;
  scrollbar-width: thin;
  scrollbar-color: #343a45 transparent;
}
.card {
  border-color: var(--stroke-soft);
  background: var(--panel);
  box-shadow: none;
}
.panel-tight {
  padding: 10px;
}
.row.actions {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
}
.label {
  color: var(--copy-3);
}
.address {
  border-color: var(--stroke-soft);
  background: #101217;
  color: var(--copy-2);
  max-height: 46px;
  overflow: hidden;
  font-size: 11px;
}
.qr-wrap {
  grid-template-columns: 64px minmax(0, 1fr);
  gap: 9px;
  border-color: var(--stroke-soft);
  background: #101217;
  padding: 8px;
}
.qr-box {
  width: 64px;
  height: 64px;
}
.qr-text {
  color: var(--copy-3);
  font-size: 12px;
}
.qr-text b { color: var(--copy); }
.metric-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}
.metric {
  grid-template-columns: 1fr;
  gap: 3px;
  min-height: 50px;
  padding: 9px;
  border: 1px solid var(--stroke-soft);
  border-radius: 8px;
  background: #101217;
}
.metric strong {
  font-size: 15px;
}
.pill {
  min-height: 24px;
  border-color: var(--stroke);
  color: var(--copy-2);
  background: #12151b;
  border-radius: 999px;
}
.pill.ok { color: var(--green); border-color: rgba(105,213,140,.34); background: rgba(105,213,140,.08); }
.pill.warn { color: var(--yellow); border-color: rgba(232,189,97,.34); background: rgba(232,189,97,.08); }
.pill.bad { color: var(--red); border-color: rgba(255,107,107,.34); background: rgba(255,107,107,.08); }
.hint {
  border-color: var(--stroke-soft);
  background: #101217;
  color: var(--copy-3);
  padding: 9px;
  font-size: 12px;
  line-height: 1.35;
}
.status-card {
  display: grid;
  gap: 10px;
}
.status-line {
  display: grid;
  grid-template-columns: 10px minmax(0, 1fr) auto;
  align-items: center;
  gap: 9px;
  min-height: 36px;
  color: var(--copy-2);
}
.status-dot {
  width: 8px;
  height: 8px;
  background: #5b6470;
}
.status-dot.ok { background: var(--green); }
.status-dot.warn { background: var(--yellow); }
.main {
  height: calc(100vh - 40px);
  grid-template-rows: 58px minmax(0, 1fr) auto;
  background: #0b0d12;
}
.topbar {
  min-height: 58px;
  padding: 0 18px;
  border-bottom: 1px solid var(--stroke-soft);
  background: #0f1116;
}
html.desktop .topbar {
  padding-right: 18px;
}
.topbar-title {
  gap: 11px;
}
.topbar-title p {
  color: var(--copy-3) !important;
}
.top-actions {
  gap: 10px;
}
.chat {
  padding: 24px 22px 18px;
  background:
    linear-gradient(180deg, rgba(101,199,255,.035), transparent 180px),
    #0b0d12;
}
.conversation {
  width: min(900px, 100%);
  gap: 16px;
}
.empty {
  width: min(560px, 100%);
  margin: 12vh auto 0;
  gap: 10px;
  text-align: center;
}
.empty h2 {
  color: var(--copy);
  font-size: 24px;
}
.empty p {
  color: var(--copy-3);
}
.steps { display: none; }
.msg {
  gap: 7px;
}
.msg.assistant {
  justify-items: start;
}
.role {
  color: var(--copy-3);
}
.bubble {
  max-width: min(760px, 100%);
  border-color: var(--stroke-soft);
  background: var(--panel);
  box-shadow: none;
}
.user .bubble {
  background: #182338;
  border-color: #283d5f;
}
.assistant .bubble {
  white-space: normal;
}
.bubble.md {
  display: grid;
  gap: 10px;
}
.bubble.md > * {
  margin: 0;
}
.bubble.md h1,
.bubble.md h2,
.bubble.md h3,
.bubble.md h4 {
  color: var(--copy);
  letter-spacing: 0;
  text-transform: none;
  line-height: 1.25;
}
.bubble.md h1 { font-size: 22px; }
.bubble.md h2 { font-size: 18px; }
.bubble.md h3 { font-size: 15px; }
.bubble.md h4 { font-size: 13px; }
.bubble.md p {
  color: var(--copy-2);
  line-height: 1.62;
}
.bubble.md strong {
  color: var(--copy);
  font-weight: 700;
}
.bubble.md a {
  color: var(--blue);
  text-decoration: none;
  border-bottom: 1px solid rgba(78, 164, 255, .32);
}
.bubble.md a:hover {
  border-bottom-color: rgba(78, 164, 255, .8);
}
.bubble.md ul,
.bubble.md ol {
  display: grid;
  gap: 6px;
  padding-left: 22px;
  color: var(--copy-2);
}
.bubble.md li {
  line-height: 1.55;
  padding-left: 2px;
}
.bubble.md blockquote {
  border-left: 3px solid rgba(78, 164, 255, .55);
  background: #101722;
  color: var(--copy-2);
  padding: 9px 11px;
  border-radius: 0 7px 7px 0;
}
.bubble.md hr {
  width: 100%;
  height: 1px;
  border: 0;
  background: var(--stroke-soft);
}
.bubble.md code {
  border: 1px solid var(--stroke-soft);
  border-radius: 5px;
  background: #0b0d12;
  color: #dce8f7;
  padding: 1px 5px;
  font-family: "Cascadia Mono", "SFMono-Regular", Consolas, monospace;
  font-size: .92em;
}
.code-block {
  overflow: hidden;
  border: 1px solid var(--stroke);
  border-radius: 8px;
  background: #090b0f;
}
.code-head {
  min-height: 32px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 0 8px 0 11px;
  border-bottom: 1px solid var(--stroke-soft);
  color: var(--copy-3);
  font-size: 11px;
}
.copy-code {
  min-height: 24px;
  padding: 0 8px;
  font-size: 11px;
}
.code-block pre {
  margin: 0;
  overflow: auto;
}
.code-block code {
  display: block;
  border: 0;
  border-radius: 0;
  background: transparent;
  padding: 12px;
  color: #dce8f7;
  white-space: pre;
  line-height: 1.5;
}
.thinking {
  width: min(760px, 100%);
  border-color: #342847;
  background: #17131f;
}
.message-meta {
  color: var(--copy-3);
}
.message-meta b {
  color: var(--blue);
}
.composer {
  padding: 12px 22px 16px;
  border-top: 1px solid var(--stroke-soft);
  background: #0f1116;
}
.composer-inner {
  width: min(900px, 100%);
  gap: 8px;
  padding: 10px;
  border-color: var(--stroke);
  background: #15171c;
  box-shadow: none;
}
.composer-top {
  gap: 12px;
}
.model-field select {
  width: min(430px, 45vw);
}
.prompt-wrap {
  border-color: var(--stroke);
  background: #101217;
}
.send {
  right: 9px;
  bottom: 9px;
  width: 42px;
  min-height: 34px;
}
.error {
  min-height: 16px;
  color: var(--red);
}
details {
  border-color: var(--stroke-soft);
  background: var(--panel);
}
summary {
  color: var(--copy-2);
}
.logbox {
  background: #101217;
  color: var(--copy-3);
}
.modal-panel {
  background: var(--panel);
  border-color: var(--stroke);
}
.modal-head {
  border-color: var(--stroke-soft);
}
.seed-word {
  background: #101217;
  border-color: var(--stroke-soft);
}
@media (max-width: 920px) {
  body { overflow: auto; }
  .window-shell { min-height: 100vh; height: auto; }
  .app {
    grid-template-columns: 1fr;
    height: auto;
    min-height: calc(100vh - 40px);
  }
  .main { height: 72vh; min-height: 560px; }
  .sidebar { border-right: 0; border-bottom: 1px solid var(--stroke-soft); }
  .metric-grid { grid-template-columns: 1fr; }
}
html.desktop .window-shell {
  height: 100vh;
  display: grid;
  grid-template-rows: 40px minmax(0, 1fr);
  overflow: hidden;
}
html.desktop .app {
  height: calc(100vh - 40px);
  min-height: 0;
}
html.desktop .main {
  height: calc(100vh - 40px);
  min-height: 0;
}
html.desktop .titlebar {
  position: static;
  inset: auto;
  z-index: 1;
}
</style>
</head>
<body>
<div class="window-shell">
<header id="titlebar" class="titlebar">
  <div id="titlebarDrag" class="titlebar-drag" data-drag-region>
    <img class="titlebar-logo" src="/assets/cocoon-favicon-32x32.png" alt="">
    <span class="titlebar-title">Cocoon</span>
  </div>
  <div class="window-controls" aria-label="Window controls">
    <button class="window-control" type="button" data-window-action="minimize" aria-label="Minimize">
      <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3 8h10" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>
    </button>
    <button class="window-control" type="button" data-window-action="toggle-maximize" aria-label="Maximize">
      <svg viewBox="0 0 16 16" aria-hidden="true"><rect x="4" y="4" width="8" height="8" rx="1.2" fill="none" stroke="currentColor" stroke-width="1.3"/></svg>
    </button>
    <button class="window-control close" type="button" data-window-action="close" aria-label="Close">
      <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M4.5 4.5l7 7m0-7l-7 7" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>
    </button>
  </div>
</header>
<div class="app">
  <aside class="sidebar">
    <div class="brand" data-drag-region>
      <div>
        <h1>Cocoon</h1>
        <small>Private AI on TON</small>
      </div>
    </div>
    <div class="side-scroll">
      <section class="card stack panel-tight">
        <div class="row between">
          <h2>Wallet</h2>
          <span id="walletStatus" class="pill">Not ready</span>
        </div>
        <div class="metric-grid">
          <div class="metric"><span>Balance</span><strong id="balanceText">0 TON</strong></div>
          <div class="metric"><span>Target</span><strong id="requiredText">20 TON</strong></div>
        </div>
        <div id="fundingHint" class="hint">Create a wallet, then fund it before connecting.</div>
        <button id="createWallet" class="primary">Create wallet</button>
      </section>

      <section class="card stack panel-tight">
        <div class="row between">
          <h2>Deposit</h2>
          <button id="refresh" class="ghost">Refresh</button>
        </div>
        <div class="label">Address</div>
        <div id="fundAddress" class="address">Create a wallet to get an address.</div>
        <div id="qrWrap" class="qr-wrap">
          <div class="qr-box"><img id="qrCode" alt="Deposit QR code"></div>
          <div class="qr-text">
            <b>TON deposit</b>
            <span>Mainnet wallet address.</span>
          </div>
        </div>
        <div class="row actions">
          <button id="copyAddress" class="ghost">Copy</button>
          <button id="showBackup" class="ghost">Show backup</button>
        </div>
      </section>

      <section class="card status-card panel-tight">
        <div class="row between">
          <h2>Network</h2>
          <span id="runnerPill" class="pill">Offline</span>
        </div>
        <div class="status-line">
          <span id="sessionDot" class="status-dot"></span>
          <span>Cocoon session</span>
          <span id="chatReady" class="pill">Waiting</span>
        </div>
        <button id="startRunner" class="primary">Connect to Cocoon</button>
        <button id="stopRunner" class="ghost">Disconnect</button>
        <div id="stateError" class="error"></div>
      </section>

      <details>
        <summary>Diagnostics</summary>
        <div id="logs" class="logbox">No logs yet</div>
      </details>
    </div>
  </aside>

  <main class="main">
    <header class="topbar">
      <div class="topbar-title" data-drag-region>
        <div>
          <h1>Cocoon Chat</h1>
          <p id="subtitle" style="color:var(--muted);font-size:13px;margin-top:3px">Qwen/Qwen3-32B over Cocoon</p>
        </div>
      </div>
      <div class="top-actions">
        <span id="modelPill" class="pill">No models</span>
        <button id="newChat" class="ghost">New chat</button>
      </div>
    </header>

    <section id="chat" class="chat">
      <div class="conversation" id="conversation"></div>
    </section>

    <form id="composer" class="composer">
      <div class="composer-inner">
        <div class="composer-top">
          <div class="model-field">
            <div class="label">Model</div>
            <select id="modelSelect"><option value="default">Default</option></select>
          </div>
        </div>
        <div class="prompt-wrap">
          <textarea id="prompt" placeholder="Message Cocoon"></textarea>
          <button id="send" class="primary send" type="submit" aria-label="Send">Go</button>
        </div>
        <div id="chatError" class="error"></div>
      </div>
    </form>
  </main>
</div>
</div>

<div id="backupModal" class="modal" aria-hidden="true">
  <section class="modal-panel" role="dialog" aria-modal="true" aria-labelledby="backupTitle">
    <div class="modal-head">
      <div>
        <h1 id="backupTitle">Save wallet backup</h1>
        <p style="color:var(--muted);font-size:13px;margin-top:5px">Save this before funding. Anyone with it can control the wallet.</p>
      </div>
      <button id="closeBackup" class="icon" type="button">x</button>
    </div>
    <div class="modal-body">
      <div class="hint">Full backup JSON includes the Cocoon node secret. The 24 words alone are not the whole app backup.</div>
      <div>
        <div class="label">Recovery phrase</div>
        <div id="seedWords" class="seed-grid"></div>
      </div>
      <div>
        <div class="label">Local file</div>
        <div id="backupPath" class="address"></div>
      </div>
      <div>
        <div class="label">Full backup JSON</div>
        <textarea id="backupJSON" class="backup-json" readonly></textarea>
      </div>
      <div class="row">
        <button id="copySeed" class="primary" type="button">Copy seed</button>
        <button id="copyBackup" type="button">Copy full backup</button>
        <button id="downloadBackup" type="button">Download JSON</button>
        <button id="savedBackup" class="ghost" type="button">I saved it</button>
      </div>
    </div>
  </section>
</div>

<script>
const $ = (id) => document.getElementById(id);
const hasNativeWindow = typeof window.cocoonWindowAction === "function";
document.documentElement.classList.toggle("desktop", hasNativeWindow);
let appState = null;
let messages = [];
let pendingBackup = null;

if (hasNativeWindow) {
  document.querySelectorAll("[data-drag-region]").forEach((region) => {
    region.addEventListener("pointerdown", (event) => {
      if (event.button === 0) window.cocoonWindowAction("drag");
    });
  });
  document.querySelectorAll("[data-window-action]").forEach((button) => {
    button.addEventListener("click", () => window.cocoonWindowAction(button.dataset.windowAction));
  });
}

function escapeHTML(value) {
  return String(value || "").replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;"
  }[ch]));
}

function sanitizeURL(value) {
  const url = String(value || "").trim();
  return /^(https?:|mailto:|ton:)/i.test(url) ? url : "";
}

function renderInlineMarkdown(value) {
  const input = String(value || "");
  const tick = String.fromCharCode(96);
  let out = "";
  let i = 0;
  while (i < input.length) {
    if (input[i] === tick) {
      const close = input.indexOf(tick, i + 1);
      if (close > i + 1) {
        out += "<code>" + escapeHTML(input.slice(i + 1, close)) + "</code>";
        i = close + 1;
        continue;
      }
    }
    if (input.slice(i, i + 2) === "**") {
      const close = input.indexOf("**", i + 2);
      if (close > i + 2) {
        out += "<strong>" + renderInlineMarkdown(input.slice(i + 2, close)) + "</strong>";
        i = close + 2;
        continue;
      }
    }
    if (input[i] === "[") {
      const labelEnd = input.indexOf("]", i + 1);
      const urlStart = labelEnd >= 0 ? input.indexOf("(", labelEnd) : -1;
      const urlEnd = urlStart >= 0 ? input.indexOf(")", urlStart) : -1;
      if (labelEnd > i + 1 && urlStart === labelEnd + 1 && urlEnd > urlStart + 1) {
        const href = sanitizeURL(input.slice(urlStart + 1, urlEnd));
        if (href) {
          out += '<a href="' + escapeHTML(href) + '" target="_blank" rel="noreferrer">' +
            renderInlineMarkdown(input.slice(i + 1, labelEnd)) + '</a>';
          i = urlEnd + 1;
          continue;
        }
      }
    }
    out += escapeHTML(input[i]);
    i += 1;
  }
  return out;
}

function renderCodeBlock(code, language) {
  const label = String(language || "text").trim().replace(/[^\w.+#-]/g, "").slice(0, 24) || "text";
  return '<div class="code-block"><div class="code-head"><span>' + escapeHTML(label) +
    '</span><button class="copy-code" type="button">Copy</button></div><pre><code>' +
    escapeHTML(code.replace(/\n+$/g, "")) + '</code></pre></div>';
}

function renderMarkdownList(list) {
  if (!list) return "";
  return "<" + list.type + ">" + list.items.map((item) => "<li>" + renderInlineMarkdown(item) + "</li>").join("") + "</" + list.type + ">";
}

function renderMarkdown(value) {
  const text = String(value || "").replace(/\r\n/g, "\n").replace(/\r/g, "\n").trim();
  if (!text) return "<p>No final answer returned.</p>";
  const lines = text.split("\n");
  const tick = String.fromCharCode(96);
  const fence = tick + tick + tick;
  const html = [];
  let paragraph = [];
  let list = null;
  let i = 0;

  const flushParagraph = () => {
    if (!paragraph.length) return;
    html.push("<p>" + paragraph.map((line) => renderInlineMarkdown(line)).join("<br>") + "</p>");
    paragraph = [];
  };
  const flushList = () => {
    if (!list) return;
    html.push(renderMarkdownList(list));
    list = null;
  };

  while (i < lines.length) {
    const line = lines[i];
    const trimmed = line.trim();
    if (!trimmed) {
      flushParagraph();
      flushList();
      i += 1;
      continue;
    }
    if (trimmed.startsWith(fence)) {
      flushParagraph();
      flushList();
      const language = trimmed.slice(fence.length).trim();
      i += 1;
      const code = [];
      while (i < lines.length && !lines[i].trim().startsWith(fence)) {
        code.push(lines[i]);
        i += 1;
      }
      if (i < lines.length) i += 1;
      html.push(renderCodeBlock(code.join("\n"), language));
      continue;
    }
    const heading = trimmed.match(/^(#{1,4})\s+(.+)$/);
    if (heading) {
      flushParagraph();
      flushList();
      const level = heading[1].length;
      html.push("<h" + level + ">" + renderInlineMarkdown(heading[2]) + "</h" + level + ">");
      i += 1;
      continue;
    }
    if (/^(-{3,}|\*{3,})$/.test(trimmed)) {
      flushParagraph();
      flushList();
      html.push("<hr>");
      i += 1;
      continue;
    }
    const bullet = trimmed.match(/^[-*]\s+(.+)$/);
    const ordered = trimmed.match(/^\d+[.)]\s+(.+)$/);
    if (bullet || ordered) {
      flushParagraph();
      const type = bullet ? "ul" : "ol";
      if (!list || list.type !== type) {
        flushList();
        list = { type: type, items: [] };
      }
      list.items.push((bullet || ordered)[1]);
      i += 1;
      continue;
    }
    if (trimmed.startsWith(">")) {
      flushParagraph();
      flushList();
      html.push("<blockquote>" + renderInlineMarkdown(trimmed.replace(/^>\s?/, "")) + "</blockquote>");
      i += 1;
      continue;
    }
    flushList();
    paragraph.push(trimmed);
    i += 1;
  }

  flushParagraph();
  flushList();
  return html.join("");
}

async function api(path, options) {
  const res = await fetch(path, options || {});
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function setPill(el, text, kind) {
  el.className = "pill" + (kind ? " " + kind : "");
  el.textContent = text;
}

function friendlyError(value) {
  const text = String(value || "");
  if (!text) return "";
  if (text.includes("waiting for a funded client channel") || text.includes("waiting for funded client channel")) {
    if (appState && appState.has_runner_state) {
      return "Cocoon channel state is saved. Reconnect is syncing; add TON only if this does not clear after a few minutes.";
    }
    return "Fund the wallet to create the first Cocoon client channel.";
  }
  if (text.includes("no ready proxy session")) {
    if (appState && appState.wallet && appState.wallet.funded) {
      return "Wallet funded. Creating the Cocoon client channel and waiting for a proxy session.";
    }
    if (appState && appState.has_runner_state) {
      return "Cocoon channel is saved. Waiting for a proxy session to become ready.";
    }
    return "Fund the wallet before connecting to Cocoon.";
  }
  if (text.includes("connectex") || text.includes("actively refused")) return "";
  if (text.includes("Failed to unpack account state")) return "Fund the wallet, then connect again.";
  if (text.includes("Service Unavailable")) return "Cocoon is not ready yet.";
  return text.replace(/\{\\"error\\":\{\\"message\\":\\"/g, "").replace(/\\".*$/g, "");
}

function canChat() {
  return !!(appState && appState.runner && appState.runner.running && appState.models && appState.models.length);
}

function hasReadyProxySession(s) {
  const stats = s && s.runner && s.runner.stats;
  const conns = stats && stats.proxy_connections;
  return Array.isArray(conns) && conns.some((conn) => conn && conn.is_ready);
}

function showBackup(backup) {
  if (!backup) return;
  pendingBackup = backup;
  $("seedWords").innerHTML = (backup.owner_mnemonic || []).map((word, index) =>
    '<div class="seed-word"><span>' + String(index + 1).padStart(2, "0") + '</span><b>' + escapeHTML(word) + '</b></div>'
  ).join("");
  $("backupPath").textContent = backup.wallet_path || "";
  $("backupJSON").value = backup.backup_json || "";
  $("backupModal").classList.add("show");
  $("backupModal").setAttribute("aria-hidden", "false");
}

function hideBackup() {
  $("backupModal").classList.remove("show");
  $("backupModal").setAttribute("aria-hidden", "true");
}

async function copyText(text, button, doneLabel) {
  if (!text) return;
  await navigator.clipboard.writeText(text);
  const old = button.textContent;
  button.textContent = doneLabel;
  setTimeout(() => button.textContent = old, 1200);
}

document.addEventListener("click", async (event) => {
  const button = event.target && event.target.closest ? event.target.closest(".copy-code") : null;
  if (!button) return;
  const block = button.closest(".code-block");
  const code = block ? block.querySelector("code") : null;
  if (code) await copyText(code.textContent, button, "Copied");
});

function formatAssistantMeta(m) {
  const parts = [];
  if (m.model && m.model !== "default") parts.push(escapeHTML(m.model));
  if (m.spend && m.spend.label) {
    parts.push("<b>Spent " + escapeHTML(m.spend.label) + "</b>");
  } else if (m.usage && m.usage.total_tokens) {
    parts.push(escapeHTML(String(m.usage.total_tokens) + " tokens"));
  }
  if (m.spend && m.spend.tokens_charged_delta) {
    parts.push(escapeHTML("charged +" + m.spend.tokens_charged_delta));
  }
  return parts.length ? '<div class="message-meta">' + parts.join(" / ") + '</div>' : "";
}

function renderThinking(m) {
  const thoughts = Array.isArray(m.thinking) ? m.thinking.filter(Boolean) : [];
  if (!thoughts.length) return "";
  return '<details class="thinking"><summary>Thinking</summary><div>' +
    escapeHTML(thoughts.join("\n\n")) + '</div></details>';
}

function renderMessageBody(m) {
  const content = m.content || m.rawFallback || "";
  if (m.role === "assistant") {
    return '<div class="bubble md">' + renderMarkdown(content || "No final answer returned.") + '</div>';
  }
  return '<div class="bubble">' + escapeHTML(content) + '</div>';
}

function renderChat() {
  const box = $("conversation");
  if (messages.length === 0) {
    box.innerHTML =
      '<div class="empty">' +
      '<div><h2>No messages yet</h2><p>Cocoon replies will appear here.</p></div>' +
      '</div>';
    return;
  }
  box.innerHTML = messages.map((m) => {
    const role = m.role === "user" ? "user" : "assistant";
    const roleLabel = role === "user" ? "You" : "Cocoon output";
    return '<div class="msg ' + role + '"><div class="role">' +
      escapeHTML(roleLabel) +
      '</div>' + renderThinking(m) +
      renderMessageBody(m) +
      (role === "assistant" ? formatAssistantMeta(m) : "") +
      '</div>';
  }).join("");
  $("chat").scrollTop = $("chat").scrollHeight;
}

function renderState(s) {
  appState = s;
  const hasWallet = !!s.wallet;
  const funded = !!(s.wallet && s.wallet.funded);
  const hasRunnerState = !!s.has_runner_state;
  const running = !!(s.runner && s.runner.running);
  const models = s.models || [];
  const channelActive = running && (models.length > 0 || hasReadyProxySession(s));
  const waitingForSession = running && !channelActive && String(s.models_error || "").includes("no ready proxy session");

  $("createWallet").disabled = hasWallet;
  $("createWallet").textContent = hasWallet ? "Wallet ready" : "Create wallet";
  $("copyAddress").disabled = !hasWallet;
  $("showBackup").disabled = !hasWallet;
  $("startRunner").disabled = !hasWallet || running;
  $("startRunner").textContent = hasRunnerState && !running ? "Reconnect to Cocoon" : "Connect to Cocoon";
  $("stopRunner").disabled = !(s.runner && s.runner.managed);
  $("prompt").disabled = !canChat();
  $("send").disabled = !canChat();
  $("modelSelect").disabled = !canChat();

  if (hasWallet) {
    $("fundAddress").textContent = s.wallet.fund_address;
    $("qrWrap").classList.add("show");
    $("qrCode").src = "/api/wallet/qr.png?address=" + encodeURIComponent(s.wallet.fund_address);
    $("balanceText").textContent = (s.wallet.balance_ton || "0") + " TON";
    $("requiredText").textContent = (s.wallet.recommended_funding_ton || "20") + " TON";
    const walletLabel = channelActive ? "Channel active" : (hasRunnerState ? "Channel saved" : (funded ? "Funded" : "Needs funding"));
    setPill($("walletStatus"), walletLabel, (funded || channelActive || hasRunnerState) ? "ok" : "warn");
    $("fundingHint").textContent = channelActive
      ? "15 TON staked in the client channel. Wallet balance is spendable."
      : (hasRunnerState
        ? "Channel state is saved. Reconnect to reuse it."
        : (funded ? "Wallet funded. Cocoon may still need a few minutes to create the client channel." : "Send 20 TON for the first channel setup. After setup, 15 TON is staked and the remainder stays spendable."));
  } else {
    $("fundAddress").textContent = "Create a wallet to get an address.";
    $("qrWrap").classList.remove("show");
    $("qrCode").removeAttribute("src");
    $("balanceText").textContent = "0 TON";
    $("requiredText").textContent = "20 TON";
    setPill($("walletStatus"), "Not ready", "bad");
    $("fundingHint").textContent = "Create a wallet, then fund it before connecting.";
  }

  if (running) {
    setPill($("runnerPill"), waitingForSession ? "Creating channel" : "Connected", waitingForSession ? "warn" : "ok");
    $("sessionDot").className = waitingForSession ? "status-dot warn" : "status-dot ok";
  } else if (funded || channelActive || hasRunnerState) {
    setPill($("runnerPill"), "Ready", "warn");
    $("sessionDot").className = "status-dot warn";
  } else {
    setPill($("runnerPill"), "Offline", "");
    $("sessionDot").className = "status-dot";
  }

  const select = $("modelSelect");
  const current = select.value || "default";
  select.innerHTML = '<option value="default">Default</option>';
  for (const m of models) {
    const opt = document.createElement("option");
    opt.value = m.id;
    opt.textContent = m.id + (m.workers ? " (" + m.workers + ")" : "");
    select.appendChild(opt);
  }
  if ([...select.options].some((o) => o.value === current)) select.value = current;
  const activeModel = select.value === "default" && models.length ? models[0].id : select.value;
  $("subtitle").textContent = canChat() ? activeModel : (hasRunnerState ? "Channel saved" : "Local wallet");
  setPill($("modelPill"), models.length ? String(models.length) + " models" : (waitingForSession ? "Connecting" : "No models"), models.length ? "ok" : "warn");
  setPill($("chatReady"), canChat() ? "Ready" : (waitingForSession ? "Starting" : "Waiting"), canChat() ? "ok" : "warn");

  const stateErr = friendlyError(s.state_error);
  const modelErr = running ? friendlyError(s.models_error) : "";
  $("stateError").textContent = stateErr || modelErr || "";
  $("chatError").textContent = modelErr || "";
}

async function refresh() {
  try {
    renderState(await api("/api/state"));
    const logs = await api("/api/logs");
    $("logs").textContent = logs.logs && logs.logs.length ? logs.logs.join("\n") : "No logs yet";
  } catch (err) {
    $("stateError").textContent = friendlyError(err.message) || err.message;
  }
}

$("createWallet").addEventListener("click", async () => {
  $("stateError").textContent = "";
  try {
    const state = await api("/api/init", { method: "POST" });
    renderState(state);
    showBackup(state.backup);
  } catch (err) {
    $("stateError").textContent = friendlyError(err.message) || err.message;
  }
});

$("startRunner").addEventListener("click", async () => {
  $("stateError").textContent = "";
  try { renderState(await api("/api/runner/start", { method: "POST" })); } catch (err) { $("stateError").textContent = friendlyError(err.message) || err.message; }
});

$("stopRunner").addEventListener("click", async () => {
  try { renderState(await api("/api/runner/stop", { method: "POST" })); } catch (err) { $("stateError").textContent = friendlyError(err.message) || err.message; }
});

$("refresh").addEventListener("click", refresh);

$("copyAddress").addEventListener("click", async () => {
  if (!appState || !appState.wallet) return;
  await copyText(appState.wallet.fund_address, $("copyAddress"), "Copied");
});

$("showBackup").addEventListener("click", async () => {
  $("stateError").textContent = "";
  try {
    const res = await api("/api/wallet/backup", { method: "POST" });
    showBackup(res.backup);
  } catch (err) {
    $("stateError").textContent = friendlyError(err.message) || err.message;
  }
});

$("copySeed").addEventListener("click", async () => {
  if (!pendingBackup) return;
  await copyText(pendingBackup.owner_mnemonic_text, $("copySeed"), "Copied");
});

$("copyBackup").addEventListener("click", async () => {
  if (!pendingBackup) return;
  await copyText(pendingBackup.backup_json, $("copyBackup"), "Copied");
});

$("downloadBackup").addEventListener("click", () => {
  if (!pendingBackup) return;
  const blob = new Blob([pendingBackup.backup_json || ""], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = "cocoon-wallet-backup.json";
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
});

$("closeBackup").addEventListener("click", hideBackup);
$("savedBackup").addEventListener("click", hideBackup);

$("newChat").addEventListener("click", () => {
  messages = [];
  renderChat();
  $("chatError").textContent = "";
});

$("composer").addEventListener("submit", async (event) => {
  event.preventDefault();
  const prompt = $("prompt").value.trim();
  if (!prompt || !canChat()) return;
  $("prompt").value = "";
  $("chatError").textContent = "";
  messages.push({ role: "user", content: prompt });
  renderChat();
  $("send").disabled = true;
  try {
    const payload = {
      model: $("modelSelect").value || "default",
      messages: messages
        .filter((m) => m.role === "user" || m.content)
        .map((m) => ({ role: m.role === "assistant" ? "assistant" : "user", content: m.content || "" }))
    };
    const res = await api("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload)
    });
    messages.push({
      role: "assistant",
      content: res.content || "",
      thinking: res.thinking || [],
      usage: res.usage || null,
      spend: res.spend || null,
      model: payload.model,
      rawFallback: res.content ? "" : JSON.stringify(res.raw, null, 2)
    });
  } catch (err) {
    const msg = friendlyError(err.message) || err.message;
    $("chatError").textContent = msg;
    messages.push({ role: "assistant", content: "Request failed: " + msg });
  } finally {
    renderChat();
    refresh();
  }
});

renderChat();
refresh();
setInterval(refresh, 5000);
</script>
</body>
</html>
`
