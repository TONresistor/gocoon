// Command gocoon is the standalone COCOON client CLI.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/TONresistor/gocoon/pkg/cocoon"
	"github.com/TONresistor/gocoon/pkg/contracts/root"
	cocoonwallet "github.com/TONresistor/gocoon/pkg/contracts/wallet"
	"github.com/TONresistor/gocoon/pkg/resources"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

type commandEntry struct {
	run     func(args []string) error
	summary string
}

// commands is the dispatch table. Initialized in init() to break the
// cycle introduced by cmdHelp referencing commands.
var commands map[string]commandEntry

func init() {
	commands = map[string]commandEntry{
		"version": {cmdVersion, "Print version"},
		"help":    {cmdHelp, "Print help"},
		"status":  {cmdStatus, "One-line summary of state"},
		"proxies": {cmdProxies, "List proxies from the local runner"},
		"root":    {cmdRoot, "Read COCOON root contract config"},
		"models":  {cmdModels, "List models exposed by all proxies"},
		"init":    {cmdInit, "Initialize standalone wallet and runner config"},
		"config":  {cmdConfig, "Generate runner config from a Cocoon wallet JSON"},
		"run":     {cmdRun, "Start a local gocoon-runner process"},
		"channel": {cmdChannel, "Channel management subcommands"},
		"wallet":  {cmdWallet, "Cocoon wallet generation and inspection"},
		"chat":    {cmdChat, "Run a chat completion"},
		"serve":   {cmdServe, "Start an OpenAI-compatible HTTP server"},
		"doctor":  {cmdDoctor, "Validate config and connectivity"},
	}
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	verb := os.Args[1]
	if verb == "--help" || verb == "-h" {
		verb = "help"
	}
	if verb == "--version" || verb == "-v" {
		verb = "version"
	}
	cmd, ok := commands[verb]
	if !ok {
		fmt.Fprintf(os.Stderr, "gocoon: unknown command %q\n", verb)
		usage()
		os.Exit(2)
	}
	if err := cmd.run(os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "gocoon:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "gocoon — COCOON client CLI")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  gocoon <command> [args]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Commands:")
	for _, name := range []string{
		"version", "help", "status", "proxies", "root",
		"models", "init", "config", "run", "channel", "wallet", "chat", "serve", "doctor",
	} {
		c := commands[name]
		fmt.Fprintf(os.Stderr, "  %-10s %s\n", name, c.summary)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Run \"gocoon help <command>\" for details.")
}

// ── command implementations ─────────────────────────────────────────────────

func cmdVersion(_ []string) error {
	fmt.Printf("gocoon %s (%s, built %s)\n", cocoon.Version, cocoon.Commit, cocoon.BuildDate)
	return nil
}

func cmdHelp(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	if args[0] == "wallet" {
		printWalletUsage(os.Stdout)
		return nil
	}
	if args[0] == "config" {
		printConfigUsage(os.Stdout)
		return nil
	}
	if args[0] == "init" {
		printInitUsage(os.Stdout)
		return nil
	}
	if args[0] == "run" {
		printRunUsage(os.Stdout)
		return nil
	}
	if args[0] == "root" {
		printRootUsage(os.Stdout)
		return nil
	}
	if cmd, ok := commands[args[0]]; ok {
		fmt.Printf("gocoon %s — %s\n", args[0], cmd.summary)
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	baseURL := fs.String("url", defaultRunnerURL, "runner base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("status: unexpected argument %q", fs.Arg(0))
	}
	return getJSONToStdout(strings.TrimRight(*baseURL, "/") + "/jsonstats")
}

func cmdProxies(_ []string) error {
	return cmdStatus(nil)
}

func cmdRoot(args []string) error {
	fs := flag.NewFlagSet("root", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "runner client-config.json path; used to find ton-config")
	tonConfigPath := fs.String("ton-config", "", "TON global config JSON path")
	rootAddress := fs.String("root", defaultMainnetRoot, "COCOON root contract address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("root: unexpected argument %q", fs.Arg(0))
	}
	resolvedTonConfig, err := resolveTONConfigPath(*configPath, *tonConfigPath)
	if err != nil {
		return err
	}
	if resolvedTonConfig == "" {
		return errors.New("root: --config or --ton-config is required")
	}
	api, err := newTONAPI(resolvedTonConfig)
	if err != nil {
		return err
	}
	rdr, err := root.NewReader(api, *rootAddress)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	summary, err := rdr.GetSummary(ctx)
	if err != nil {
		return err
	}
	params, err := rdr.GetParams(ctx)
	if err != nil {
		return err
	}
	out := rootOutput{
		RootAddress:              rdr.Address().String(),
		TONConfigPath:            resolvedTonConfig,
		Version:                  summary.Version,
		LastProxySeqno:           summary.LastProxySeqno,
		ParamsVersion:            summary.ParamsVersion,
		UniqueID:                 summary.UniqueID,
		IsTest:                   summary.IsTest,
		OwnerAddress:             addressString(summary.OwnerAddress),
		PricePerTokenNano:        summary.PricePerToken.String(),
		WorkerFeePerTokenNano:    summary.WorkerFeePerToken.String(),
		MinProxyStakeNano:        summary.MinProxyStake.String(),
		MinProxyStakeTON:         formatNanoTON(summary.MinProxyStake),
		MinClientStakeNano:       summary.MinClientStake.String(),
		MinClientStakeTON:        formatNanoTON(summary.MinClientStake),
		CachedTokensPriceMult:    params.CachedTokensPriceMult,
		ReasoningTokensPriceMult: params.ReasoningTokensPriceMult,
		ProxyDelayBeforeClose:    params.ProxyDelayBeforeClose,
		ClientDelayBeforeClose:   params.ClientDelayBeforeClose,
		ProxySCHash:              params.ProxySCHash.Text(16),
		WorkerSCHash:             params.WorkerSCHash.Text(16),
		ClientSCHash:             params.ClientSCHash.Text(16),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func printRootUsage(out *os.File) {
	fmt.Fprintln(out, "gocoon root — read COCOON root contract config")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  gocoon root --config ./gocoon-data/client-config.json")
	fmt.Fprintln(out, "  gocoon root --ton-config ./global.config.json [--root <address>]")
}

func addressString(addr *address.Address) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

func cmdModels(args []string) error {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	baseURL := fs.String("url", defaultRunnerURL, "runner base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("models: unexpected argument %q", fs.Arg(0))
	}
	return getJSONToStdout(strings.TrimRight(*baseURL, "/") + "/v1/models")
}

const (
	defaultRunnerURL             = "http://127.0.0.1:10000"
	defaultMainnetRoot           = "EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k"
	recommendedCocoonFundingNano = uint64(20_000_000_000)
	canonicalMinClientStakeNano  = uint64(15_000_000_000)
)

type storedWallet struct {
	OwnerAddress     string `json:"ownerAddress"`
	NodeSecretBase64 string `json:"nodeSecretBase64"`
	NodeAddress      string `json:"nodeAddress"`
}

type runnerConfigJSON struct {
	IsTest            bool   `json:"is_test"`
	IsTestnet         bool   `json:"is_testnet"`
	HTTPPort          int    `json:"http_port"`
	RPCPort           int    `json:"rpc_port,omitempty"`
	ProxyConnections  int    `json:"proxy_connections"`
	TonConfigFilename string `json:"ton_config_filename"`
	OwnerAddress      string `json:"owner_address"`
	RootContractAddr  string `json:"root_contract_address"`
	NodeWalletKey     string `json:"node_wallet_key"`
	MaxCoefficient    int    `json:"max_coefficient"`
	MaxTokens         int    `json:"max_tokens"`
}

type rootOutput struct {
	RootAddress              string `json:"root_address"`
	TONConfigPath            string `json:"ton_config_path"`
	Version                  uint32 `json:"version"`
	LastProxySeqno           uint32 `json:"last_proxy_seqno"`
	ParamsVersion            uint32 `json:"params_version"`
	UniqueID                 uint32 `json:"unique_id"`
	IsTest                   bool   `json:"is_test"`
	OwnerAddress             string `json:"owner_address"`
	PricePerTokenNano        string `json:"price_per_token_nano"`
	WorkerFeePerTokenNano    string `json:"worker_fee_per_token_nano"`
	MinProxyStakeNano        string `json:"min_proxy_stake_nano"`
	MinProxyStakeTON         string `json:"min_proxy_stake_ton"`
	MinClientStakeNano       string `json:"min_client_stake_nano"`
	MinClientStakeTON        string `json:"min_client_stake_ton"`
	CachedTokensPriceMult    uint32 `json:"cached_tokens_price_multiplier"`
	ReasoningTokensPriceMult uint32 `json:"reasoning_tokens_price_multiplier"`
	ProxyDelayBeforeClose    uint32 `json:"proxy_delay_before_close"`
	ClientDelayBeforeClose   uint32 `json:"client_delay_before_close"`
	ProxySCHash              string `json:"proxy_sc_hash_hex"`
	WorkerSCHash             string `json:"worker_sc_hash_hex"`
	ClientSCHash             string `json:"client_sc_hash_hex"`
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", ".", "directory for wallet.json, ton-config.json, and client-config.json")
	walletPath := fs.String("wallet-out", "", "wallet output path; default <dir>/wallet.json")
	configPath := fs.String("config-out", "", "runner config output path; default <dir>/client-config.json")
	tonConfigPath := fs.String("ton-config", "", "existing TON global config path; default writes embedded config to <dir>/ton-config.json")
	walletCodePath := fs.String("wallet-code", "", "optional cocoon-wallet.code.json override")
	httpPort := fs.Int("http-port", 10000, "runner HTTP port")
	rpcPort := fs.Int("rpc-port", 10001, "runner RPC port")
	rootAddress := fs.String("root", defaultMainnetRoot, "COCOON root contract address")
	force := fs.Bool("force", false, "overwrite existing output files")
	jsonOutput := fs.Bool("json", false, "print machine-readable setup summary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("init: unexpected argument %q", fs.Arg(0))
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return fmt.Errorf("init: create dir: %w", err)
	}
	defaultPath := func(name string) string {
		return filepath.Join(*dir, name)
	}
	if *walletPath == "" {
		*walletPath = defaultPath("wallet.json")
	}
	if *configPath == "" {
		*configPath = defaultPath("client-config.json")
	}
	resolvedTonConfig := *tonConfigPath
	writeTonConfig := resolvedTonConfig == ""
	if writeTonConfig {
		resolvedTonConfig = defaultPath("ton-config.json")
	} else if _, err := os.Stat(resolvedTonConfig); err != nil {
		return fmt.Errorf("init: ton config: %w", err)
	}
	for _, outputPath := range []string{*walletPath, *configPath} {
		if err := preflightOutputFile(outputPath, *force); err != nil {
			return err
		}
	}
	if writeTonConfig {
		if err := preflightOutputFile(resolvedTonConfig, *force); err != nil {
			return err
		}
	}

	extra := []string{}
	if *walletCodePath != "" {
		extra = append(extra, *walletCodePath)
	}
	code, _, err := cocoonwallet.LoadDefaultCode(extra...)
	if err != nil {
		return err
	}
	generated, err := cocoonwallet.Generate(cocoonwallet.GenerateOptions{Code: code})
	if err != nil {
		return err
	}
	walletJSON, err := json.MarshalIndent(generated, "", "  ")
	if err != nil {
		return err
	}
	walletJSON = append(walletJSON, '\n')
	if err := writeOutputFile(*walletPath, walletJSON, 0o600, *force); err != nil {
		return err
	}

	if writeTonConfig {
		if err := writeOutputFile(resolvedTonConfig, resources.TONConfigJSON, 0o600, *force); err != nil {
			return err
		}
	}
	absTonConfig, err := filepath.Abs(resolvedTonConfig)
	if err != nil {
		return err
	}
	cfg := runnerConfigJSON{
		IsTest:            false,
		IsTestnet:         false,
		HTTPPort:          *httpPort,
		RPCPort:           *rpcPort,
		ProxyConnections:  1,
		TonConfigFilename: absTonConfig,
		OwnerAddress:      generated.OwnerAddress,
		RootContractAddr:  *rootAddress,
		NodeWalletKey:     generated.NodeSecretBase64,
		MaxCoefficient:    0,
		MaxTokens:         0,
	}
	configJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	configJSON = append(configJSON, '\n')
	if err := writeOutputFile(*configPath, configJSON, 0o600, *force); err != nil {
		return err
	}

	summary := initSummary{
		WalletPath:                  *walletPath,
		TONConfigPath:               resolvedTonConfig,
		ConfigPath:                  *configPath,
		OwnerAddress:                generated.OwnerAddress,
		FundAddress:                 generated.NodeAddress,
		RecommendedFundingNano:      strconv.FormatUint(recommendedCocoonFundingNano, 10),
		RecommendedFundingTON:       formatNanoTON(new(big.Int).SetUint64(recommendedCocoonFundingNano)),
		CanonicalMinClientStakeNano: strconv.FormatUint(canonicalMinClientStakeNano, 10),
		NextCommands: []string{
			"gocoon wallet wait-funded --wallet " + *walletPath + " --config " + *configPath,
			"gocoon run --config " + *configPath,
		},
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	}
	printInitSummary(os.Stdout, summary)
	return nil
}

type initSummary struct {
	WalletPath                  string   `json:"wallet_path"`
	TONConfigPath               string   `json:"ton_config_path"`
	ConfigPath                  string   `json:"config_path"`
	OwnerAddress                string   `json:"owner_address"`
	FundAddress                 string   `json:"fund_address"`
	RecommendedFundingNano      string   `json:"recommended_funding_nano"`
	RecommendedFundingTON       string   `json:"recommended_funding_ton"`
	CanonicalMinClientStakeNano string   `json:"canonical_min_client_stake_nano"`
	NextCommands                []string `json:"next_commands"`
}

func printInitSummary(out io.Writer, s initSummary) {
	fmt.Fprintln(out, "created:")
	fmt.Fprintln(out, "  wallet:", s.WalletPath)
	fmt.Fprintln(out, "  ton_config:", s.TONConfigPath)
	fmt.Fprintln(out, "  config:", s.ConfigPath)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "fund this Cocoon wallet before starting the runner:")
	fmt.Fprintln(out, "  address:", s.FundAddress)
	fmt.Fprintln(out, "  amount:", s.RecommendedFundingTON, "TON")
	fmt.Fprintln(out, "  amount_nano:", s.RecommendedFundingNano)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "next:")
	for _, cmd := range s.NextCommands {
		fmt.Fprintln(out, " ", cmd)
	}
}

func printInitUsage(out *os.File) {
	fmt.Fprintln(out, "gocoon init — create standalone wallet and runner config")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  gocoon init --dir ./gocoon-data [--json]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Fund the printed Cocoon wallet address with 20 TON, then run:")
	fmt.Fprintln(out, "  gocoon wallet wait-funded --wallet ./gocoon-data/wallet.json --config ./gocoon-data/client-config.json")
	fmt.Fprintln(out, "  gocoon run --config ./gocoon-data/client-config.json")
}

func cmdConfig(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printConfigUsage(os.Stdout)
		return nil
	}
	switch args[0] {
	case "generate":
		return cmdConfigGenerate(args[1:])
	default:
		return fmt.Errorf("unknown config verb %q", args[0])
	}
}

func cmdConfigGenerate(args []string) error {
	fs := flag.NewFlagSet("config generate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	walletPath := fs.String("wallet", "", "path to wallet JSON from `gocoon wallet generate`")
	tonConfigPath := fs.String("ton-config", "", "path to TON global config JSON")
	outPath := fs.String("out", "client-config.json", "output client-config.json path")
	httpPort := fs.Int("http-port", 10000, "runner HTTP port")
	rpcPort := fs.Int("rpc-port", 10001, "runner RPC port")
	rootAddress := fs.String("root", defaultMainnetRoot, "COCOON root contract address")
	proxyConnections := fs.Int("proxy-connections", 1, "number of proxy connections")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("config generate: unexpected argument %q", fs.Arg(0))
	}
	if *walletPath == "" {
		return errors.New("config generate: --wallet is required")
	}
	if *tonConfigPath == "" {
		return errors.New("config generate: --ton-config is required")
	}
	if _, err := os.Stat(*tonConfigPath); err != nil {
		return fmt.Errorf("config generate: ton config: %w", err)
	}
	raw, err := os.ReadFile(*walletPath)
	if err != nil {
		return fmt.Errorf("config generate: read wallet: %w", err)
	}
	var wallet storedWallet
	if err := json.Unmarshal(raw, &wallet); err != nil {
		return fmt.Errorf("config generate: parse wallet: %w", err)
	}
	if wallet.OwnerAddress == "" || wallet.NodeSecretBase64 == "" {
		return errors.New("config generate: wallet JSON missing ownerAddress or nodeSecretBase64")
	}
	tonPath, err := filepath.Abs(*tonConfigPath)
	if err != nil {
		return err
	}
	cfg := runnerConfigJSON{
		IsTest:            false,
		IsTestnet:         false,
		HTTPPort:          *httpPort,
		RPCPort:           *rpcPort,
		ProxyConnections:  *proxyConnections,
		TonConfigFilename: tonPath,
		OwnerAddress:      wallet.OwnerAddress,
		RootContractAddr:  *rootAddress,
		NodeWalletKey:     wallet.NodeSecretBase64,
		MaxCoefficient:    0,
		MaxTokens:         0,
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if err := os.WriteFile(*outPath, out, 0o600); err != nil {
		return fmt.Errorf("config generate: write %s: %w", *outPath, err)
	}
	fmt.Println(*outPath)
	return nil
}

func writeOutputFile(path string, data []byte, mode os.FileMode, force bool) error {
	if err := preflightOutputFile(path, force); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, mode)
}

func preflightOutputFile(path string, force bool) error {
	if force {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; pass --force to overwrite", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func printConfigUsage(out *os.File) {
	fmt.Fprintln(out, "gocoon config — standalone runner configuration")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  gocoon config generate --wallet wallet.json --ton-config global.config.json [--out client-config.json]")
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "client-config.json", "path to client-config.json")
	runnerPath := fs.String("runner", "", "path to gocoon-runner binary")
	verbosity := fs.Int("v", 3, "runner verbosity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("run: unexpected argument %q", fs.Arg(0))
	}
	path, err := resolveRunnerPath(*runnerPath)
	if err != nil {
		return err
	}
	cmd := exec.Command(path, "--config", *configPath, "-v"+strconv.Itoa(*verbosity))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func printRunUsage(out *os.File) {
	fmt.Fprintln(out, "gocoon run — start the local gocoon-runner")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  gocoon run --config client-config.json [--runner ./gocoon-runner] [-v 3]")
}

func resolveRunnerPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if p, err := exec.LookPath("gocoon-runner"); err == nil {
		return p, nil
	}
	self, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(self), "gocoon-runner")
		if st, statErr := os.Stat(candidate); statErr == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("gocoon-runner not found in PATH or next to gocoon; pass --runner")
}

func cmdChannel(args []string) error {
	if len(args) == 0 {
		fmt.Println("gocoon channel <list|info|topup|withdraw|close>")
		return nil
	}
	switch args[0] {
	case "list":
		return cmdStatus(args[1:])
	case "info":
		return cmdChannelInfo(args[1:])
	case "topup", "withdraw", "close":
		return cmdChannelRequest(args[0], args[1:])
	}
	return fmt.Errorf("unknown channel verb %q", args[0])
}

func cmdChannelInfo(args []string) error {
	fs := flag.NewFlagSet("channel info", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	baseURL := fs.String("url", defaultRunnerURL, "runner base URL")
	clientSC := fs.String("client-sc", "", "cocoon_client smart-contract address; defaults to first ready runner session")
	configPath := fs.String("config", "", "runner client-config.json path; used to find ton-config")
	tonConfigPath := fs.String("ton-config", "", "TON global config JSON path")
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("channel info: unexpected argument %q", fs.Arg(0))
	}
	addr := strings.TrimSpace(*clientSC)
	if addr == "" {
		var err error
		addr, err = firstClientSC(strings.TrimRight(*baseURL, "/"))
		if err != nil {
			return err
		}
	}
	resolvedTonConfig, err := resolveTONConfigPath(*configPath, *tonConfigPath)
	if err != nil {
		return err
	}
	if resolvedTonConfig == "" {
		return errors.New("channel info: --config or --ton-config is required")
	}
	info, err := fetchClientSCInfo(context.Background(), resolvedTonConfig, addr)
	if err != nil {
		return err
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	printClientSCInfo(os.Stdout, info)
	return nil
}

func cmdChannelRequest(verb string, args []string) error {
	fs := flag.NewFlagSet("channel "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	baseURL := fs.String("url", defaultRunnerURL, "runner base URL")
	clientSC := fs.String("client-sc", "", "cocoon_client smart-contract address; defaults to first ready runner session")
	amount := fs.String("amount", "", "topup amount in nanoTON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("channel %s: unexpected argument %q", verb, fs.Arg(0))
	}
	addr := strings.TrimSpace(*clientSC)
	if addr == "" {
		var err error
		addr, err = firstClientSC(strings.TrimRight(*baseURL, "/"))
		if err != nil {
			return err
		}
	}
	pathVerb := verb
	if verb == "close" {
		pathVerb = "close"
	}
	reqURL := strings.TrimRight(*baseURL, "/") + "/request/" + pathVerb + "?proxy=" + url.QueryEscape(addr)
	if verb == "topup" && *amount != "" {
		reqURL += "&amount=" + url.QueryEscape(*amount)
	}
	resp, err := http.Get(reqURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("channel %s: runner returned %s: %s", verb, resp.Status, strings.TrimSpace(string(body)))
	}
	_, err = os.Stdout.Write(body)
	return err
}

func firstClientSC(baseURL string) (string, error) {
	body, err := getURL(baseURL + "/jsonstats")
	if err != nil {
		return "", err
	}
	var stats struct {
		Proxies []struct {
			SCAddress string `json:"sc_address"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(body, &stats); err != nil {
		return "", err
	}
	for _, p := range stats.Proxies {
		if strings.TrimSpace(p.SCAddress) != "" {
			return p.SCAddress, nil
		}
	}
	return "", errors.New("no ready cocoon_client address in runner /jsonstats; pass --client-sc")
}

type clientSCInfo struct {
	Address       string `json:"address"`
	AccountStatus string `json:"account_status"`
	State         uint8  `json:"state"`
	StateName     string `json:"state_name"`
	BalanceNano   string `json:"balance_nano"`
	BalanceTON    string `json:"balance_ton"`
	StakeNano     string `json:"stake_nano"`
	StakeTON      string `json:"stake_ton"`
	TokensUsed    uint64 `json:"tokens_used"`
	UnlockTs      uint32 `json:"unlock_ts"`
}

func fetchClientSCInfo(ctx context.Context, tonConfigPath, clientSCAddr string) (*clientSCInfo, error) {
	api, err := newTONAPI(tonConfigPath)
	if err != nil {
		return nil, err
	}
	addr, err := address.ParseAddr(clientSCAddr)
	if err != nil {
		return nil, fmt.Errorf("channel info: parse client-sc: %w", err)
	}
	mc, err := api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("channel info: masterchain info: %w", err)
	}
	acc, err := api.GetAccount(ctx, mc, addr)
	if err != nil {
		return nil, fmt.Errorf("channel info: get account: %w", err)
	}
	out := &clientSCInfo{
		Address:       clientSCAddr,
		AccountStatus: "missing",
		StateName:     "missing",
		BalanceNano:   "0",
		BalanceTON:    "0",
		StakeNano:     "0",
		StakeTON:      "0",
	}
	if acc == nil || acc.State == nil {
		return out, nil
	}
	out.AccountStatus = fmt.Sprint(acc.State.Status)
	if acc.Data == nil {
		return out, nil
	}
	parsed, err := parseClientSCData(acc.Data)
	if err != nil {
		return nil, err
	}
	out.State = parsed.State
	out.StateName = clientStateName(parsed.State)
	out.BalanceNano = parsed.Balance.String()
	out.BalanceTON = formatNanoTON(parsed.Balance)
	out.StakeNano = parsed.Stake.String()
	out.StakeTON = formatNanoTON(parsed.Stake)
	out.TokensUsed = parsed.TokensUsed
	out.UnlockTs = parsed.UnlockTs
	return out, nil
}

type parsedClientSCData struct {
	State      uint8
	Balance    *big.Int
	Stake      *big.Int
	TokensUsed uint64
	UnlockTs   uint32
}

func parseClientSCData(data cellData) (*parsedClientSCData, error) {
	s := data.BeginParse()
	state, err := s.LoadUInt(2)
	if err != nil {
		return nil, fmt.Errorf("channel info: parse state: %w", err)
	}
	balance, err := s.LoadBigCoins()
	if err != nil {
		return nil, fmt.Errorf("channel info: parse balance: %w", err)
	}
	stake, err := s.LoadBigCoins()
	if err != nil {
		return nil, fmt.Errorf("channel info: parse stake: %w", err)
	}
	tokensUsed, err := s.LoadUInt(64)
	if err != nil {
		return nil, fmt.Errorf("channel info: parse tokens_used: %w", err)
	}
	unlockTs, err := s.LoadUInt(32)
	if err != nil {
		return nil, fmt.Errorf("channel info: parse unlock_ts: %w", err)
	}
	return &parsedClientSCData{
		State:      uint8(state),
		Balance:    balance,
		Stake:      stake,
		TokensUsed: tokensUsed,
		UnlockTs:   uint32(unlockTs),
	}, nil
}

type cellData interface {
	BeginParse() *cell.Slice
}

func clientStateName(state uint8) string {
	switch state {
	case 0:
		return "active"
	case 1:
		return "closing"
	case 2:
		return "closed"
	default:
		return "unknown"
	}
}

func printClientSCInfo(out io.Writer, info *clientSCInfo) {
	fmt.Fprintln(out, "client_sc:", info.Address)
	fmt.Fprintln(out, "account_status:", info.AccountStatus)
	fmt.Fprintln(out, "state:", info.StateName)
	fmt.Fprintln(out, "state_code:", info.State)
	fmt.Fprintln(out, "balance:", info.BalanceTON, "TON")
	fmt.Fprintln(out, "balance_nano:", info.BalanceNano)
	fmt.Fprintln(out, "stake:", info.StakeTON, "TON")
	fmt.Fprintln(out, "stake_nano:", info.StakeNano)
	fmt.Fprintln(out, "tokens_used:", info.TokensUsed)
	fmt.Fprintln(out, "unlock_ts:", info.UnlockTs)
}

func cmdWallet(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printWalletUsage(os.Stdout)
		return nil
	}
	switch args[0] {
	case "generate":
		return cmdWalletGenerate(args[1:])
	case "info":
		return cmdWalletInfo(args[1:])
	case "wait-funded":
		return cmdWalletWaitFunded(args[1:])
	case "withdraw", "drain":
		return cmdWalletWithdraw(args[1:])
	default:
		return fmt.Errorf("unknown wallet verb %q", args[0])
	}
}

func cmdWalletGenerate(args []string) error {
	fs := flag.NewFlagSet("wallet generate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	walletCodePath := fs.String("wallet-code", "", "path to cocoon-wallet.code.json")
	pretty := fs.Bool("pretty", false, "pretty-print JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("wallet generate: unexpected argument %q", fs.Arg(0))
	}

	extra := []string{}
	if *walletCodePath != "" {
		extra = append(extra, *walletCodePath)
	}
	code, _, err := cocoonwallet.LoadDefaultCode(extra...)
	if err != nil {
		return err
	}
	generated, err := cocoonwallet.Generate(cocoonwallet.GenerateOptions{Code: code})
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	if *pretty {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(generated)
}

func cmdWalletInfo(args []string) error {
	fs := flag.NewFlagSet("wallet info", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	walletPath := fs.String("wallet", "wallet.json", "path to wallet JSON")
	configPath := fs.String("config", "", "runner client-config.json path; used to find ton-config")
	tonConfigPath := fs.String("ton-config", "", "TON global config JSON path for on-chain balance/root reads")
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("wallet info: unexpected argument %q", fs.Arg(0))
	}
	info, err := loadWalletInfo(*walletPath)
	if err != nil {
		return err
	}
	out := walletInfoOutput{
		WalletPath:             *walletPath,
		OwnerAddress:           info.OwnerAddress,
		FundAddress:            info.NodeAddress,
		RecommendedFundingNano: strconv.FormatUint(recommendedCocoonFundingNano, 10),
		RecommendedFundingTON:  formatNanoTON(new(big.Int).SetUint64(recommendedCocoonFundingNano)),
	}
	resolvedTonConfig, err := resolveTONConfigPath(*configPath, *tonConfigPath)
	if err != nil {
		return err
	}
	if resolvedTonConfig != "" {
		status, err := fetchFundingStatus(context.Background(), resolvedTonConfig, info.NodeAddress)
		if err != nil {
			return err
		}
		out.TONConfigPath = resolvedTonConfig
		out.BalanceNano = status.BalanceNano.String()
		out.BalanceTON = formatNanoTON(status.BalanceNano)
		funded := status.BalanceNano.Cmp(new(big.Int).SetUint64(recommendedCocoonFundingNano)) >= 0
		out.Funded = &funded
		if status.MinClientStakeNano != nil {
			out.OnChainMinClientStakeNano = status.MinClientStakeNano.String()
			out.OnChainMinClientStakeTON = formatNanoTON(status.MinClientStakeNano)
		}
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	printWalletInfo(os.Stdout, out)
	return nil
}

func cmdWalletWaitFunded(args []string) error {
	fs := flag.NewFlagSet("wallet wait-funded", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	walletPath := fs.String("wallet", "wallet.json", "path to wallet JSON")
	configPath := fs.String("config", "", "runner client-config.json path; used to find ton-config")
	tonConfigPath := fs.String("ton-config", "", "TON global config JSON path")
	minNano := fs.String("min-nano", strconv.FormatUint(recommendedCocoonFundingNano, 10), "minimum funded balance in nanoTON")
	timeout := fs.Duration("timeout", 10*time.Minute, "maximum wait duration")
	poll := fs.Duration("poll", 2*time.Second, "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("wallet wait-funded: unexpected argument %q", fs.Arg(0))
	}
	if *timeout <= 0 {
		return errors.New("wallet wait-funded: --timeout must be positive")
	}
	if *poll <= 0 {
		return errors.New("wallet wait-funded: --poll must be positive")
	}
	min, ok := new(big.Int).SetString(*minNano, 10)
	if !ok || min.Sign() <= 0 {
		return fmt.Errorf("wallet wait-funded: invalid --min-nano %q", *minNano)
	}
	info, err := loadWalletInfo(*walletPath)
	if err != nil {
		return err
	}
	resolvedTonConfig, err := resolveTONConfigPath(*configPath, *tonConfigPath)
	if err != nil {
		return err
	}
	if resolvedTonConfig == "" {
		return errors.New("wallet wait-funded: --config or --ton-config is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	for {
		status, err := fetchFundingStatus(ctx, resolvedTonConfig, info.NodeAddress)
		if err == nil {
			fmt.Printf("wallet balance: %s TON (%s nanoTON), required: %s TON\n",
				formatNanoTON(status.BalanceNano), status.BalanceNano.String(), formatNanoTON(min))
			if status.BalanceNano.Cmp(min) >= 0 {
				fmt.Println("wallet funded: yes")
				return nil
			}
		} else {
			fmt.Fprintln(os.Stderr, "wallet wait-funded:", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wallet wait-funded: timeout waiting for %s to reach %s nanoTON", info.NodeAddress, min.String())
		case <-ticker.C:
		}
	}
}

func cmdWalletWithdraw(args []string) error {
	fs := flag.NewFlagSet("wallet withdraw", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	walletPath := fs.String("wallet", "wallet.json", "path to wallet JSON")
	configPath := fs.String("config", "", "runner client-config.json path; used to find ton-config")
	tonConfigPath := fs.String("ton-config", "", "TON global config JSON path")
	toAddress := fs.String("to", "", "destination TON address")
	timeout := fs.Duration("timeout", 2*time.Minute, "maximum time to wait for confirmation")
	poll := fs.Duration("poll", time.Second, "confirmation poll interval")
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("wallet withdraw: unexpected argument %q", fs.Arg(0))
	}
	if strings.TrimSpace(*toAddress) == "" {
		return errors.New("wallet withdraw: --to is required")
	}
	if *timeout <= 0 {
		return errors.New("wallet withdraw: --timeout must be positive")
	}
	if *poll <= 0 {
		return errors.New("wallet withdraw: --poll must be positive")
	}
	rawWallet, err := loadStoredWallet(*walletPath)
	if err != nil {
		return err
	}
	nodeWallet, owner, nodeKey, code, err := nodeWalletFromStored(rawWallet)
	if err != nil {
		return err
	}
	to, err := address.ParseAddr(strings.TrimSpace(*toAddress))
	if err != nil {
		return fmt.Errorf("wallet withdraw: parse --to: %w", err)
	}
	resolvedTonConfig, err := resolveTONConfigPath(*configPath, *tonConfigPath)
	if err != nil {
		return err
	}
	if resolvedTonConfig == "" {
		return errors.New("wallet withdraw: --config or --ton-config is required")
	}
	api, err := newTONAPI(resolvedTonConfig)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	out, err := withdrawWallet(ctx, api, nodeWallet, owner, nodeKey, code, to, *poll)
	if err != nil {
		return err
	}
	out.WalletPath = *walletPath
	out.TONConfigPath = resolvedTonConfig
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	printWalletWithdraw(os.Stdout, out)
	return nil
}

type walletInfo struct {
	OwnerAddress string
	NodeAddress  string
}

type walletWithdrawOutput struct {
	WalletPath     string `json:"wallet_path,omitempty"`
	TONConfigPath  string `json:"ton_config_path,omitempty"`
	FromAddress    string `json:"from_address"`
	ToAddress      string `json:"to_address"`
	Mode           uint8  `json:"mode"`
	Seqno          uint32 `json:"seqno"`
	WasDeployed    bool   `json:"was_deployed"`
	BalanceNano    string `json:"balance_nano"`
	BalanceTON     string `json:"balance_ton"`
	ExternalHash   string `json:"external_hash"`
	InMessageHash  string `json:"in_message_hash"`
	Confirmed      bool   `json:"confirmed"`
	Confirmation   string `json:"confirmation"`
	TransactionLT  uint64 `json:"transaction_lt,omitempty"`
	TransactionHex string `json:"transaction_hash,omitempty"`
}

type walletInfoOutput struct {
	WalletPath                string `json:"wallet_path"`
	TONConfigPath             string `json:"ton_config_path,omitempty"`
	OwnerAddress              string `json:"owner_address"`
	FundAddress               string `json:"fund_address"`
	RecommendedFundingNano    string `json:"recommended_funding_nano"`
	RecommendedFundingTON     string `json:"recommended_funding_ton"`
	BalanceNano               string `json:"balance_nano,omitempty"`
	BalanceTON                string `json:"balance_ton,omitempty"`
	Funded                    *bool  `json:"funded,omitempty"`
	OnChainMinClientStakeNano string `json:"on_chain_min_client_stake_nano,omitempty"`
	OnChainMinClientStakeTON  string `json:"on_chain_min_client_stake_ton,omitempty"`
}

type fundingStatus struct {
	BalanceNano        *big.Int
	MinClientStakeNano *big.Int
}

func loadWalletInfo(path string) (*walletInfo, error) {
	wallet, err := loadStoredWallet(path)
	if err != nil {
		return nil, err
	}
	nodeAddress := strings.TrimSpace(wallet.NodeAddress)
	if nodeAddress == "" {
		derived, err := deriveNodeAddressFromWallet(wallet)
		if err != nil {
			return nil, err
		}
		nodeAddress = derived
	}
	return &walletInfo{OwnerAddress: wallet.OwnerAddress, NodeAddress: nodeAddress}, nil
}

func loadStoredWallet(path string) (storedWallet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return storedWallet{}, fmt.Errorf("wallet: read %s: %w", path, err)
	}
	var wallet storedWallet
	if err := json.Unmarshal(raw, &wallet); err != nil {
		return storedWallet{}, fmt.Errorf("wallet: parse %s: %w", path, err)
	}
	if wallet.OwnerAddress == "" || wallet.NodeSecretBase64 == "" {
		return storedWallet{}, errors.New("wallet: JSON missing ownerAddress or nodeSecretBase64")
	}
	return wallet, nil
}

func deriveNodeAddressFromWallet(wallet storedWallet) (string, error) {
	owner, err := address.ParseAddr(wallet.OwnerAddress)
	if err != nil {
		return "", fmt.Errorf("wallet: parse ownerAddress: %w", err)
	}
	seed, err := decodeNodeSeed(wallet.NodeSecretBase64)
	if err != nil {
		return "", err
	}
	code, _, err := cocoonwallet.LoadDefaultCode()
	if err != nil {
		return "", err
	}
	key := ed25519.NewKeyFromSeed(seed)
	nodeAddress, err := cocoonwallet.DeriveAddress(cocoonwallet.Config{
		PublicKey:    key.Public().(ed25519.PublicKey),
		OwnerAddress: owner,
	}, code)
	if err != nil {
		return "", fmt.Errorf("wallet: derive node address: %w", err)
	}
	return nodeAddress.String(), nil
}

func nodeWalletFromStored(wallet storedWallet) (*address.Address, *address.Address, ed25519.PrivateKey, *cell.Cell, error) {
	owner, err := address.ParseAddr(wallet.OwnerAddress)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("wallet: parse ownerAddress: %w", err)
	}
	seed, err := decodeNodeSeed(wallet.NodeSecretBase64)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	key := ed25519.NewKeyFromSeed(seed)
	code, _, err := cocoonwallet.LoadDefaultCode()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var nodeAddress *address.Address
	if strings.TrimSpace(wallet.NodeAddress) != "" {
		nodeAddress, err = address.ParseAddr(wallet.NodeAddress)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("wallet: parse nodeAddress: %w", err)
		}
	} else {
		nodeAddress, err = cocoonwallet.DeriveAddress(cocoonwallet.Config{
			PublicKey:    key.Public().(ed25519.PublicKey),
			OwnerAddress: owner,
		}, code)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("wallet: derive node address: %w", err)
		}
	}
	return nodeAddress, owner, key, code, nil
}

func decodeNodeSeed(s string) ([]byte, error) {
	seed, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		seed, err = base64.URLEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("wallet: nodeSecretBase64 not valid base64: %w", err)
		}
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("wallet: nodeSecretBase64 decoded length %d, want %d", len(seed), ed25519.SeedSize)
	}
	return seed, nil
}

const walletWithdrawMode = uint8(128 + 32)

func withdrawWallet(ctx context.Context, api ton.APIClientWrapped, nodeWallet, owner *address.Address, nodeKey ed25519.PrivateKey, code *cell.Cell, to *address.Address, poll time.Duration) (*walletWithdrawOutput, error) {
	if api == nil {
		return nil, errors.New("wallet withdraw: nil TON API")
	}
	if nodeWallet == nil {
		return nil, errors.New("wallet withdraw: nil source wallet")
	}
	if owner == nil {
		return nil, errors.New("wallet withdraw: nil owner wallet")
	}
	if len(nodeKey) != ed25519.PrivateKeySize {
		return nil, errors.New("wallet withdraw: invalid node private key")
	}
	if to == nil {
		return nil, errors.New("wallet withdraw: nil destination")
	}
	if poll <= 0 {
		poll = time.Second
	}
	sourceBefore, err := fetchAccountSnapshot(ctx, api, nodeWallet)
	if err != nil {
		return nil, fmt.Errorf("wallet withdraw: source balance: %w", err)
	}
	if sourceBefore.Balance.Sign() == 0 {
		return nil, fmt.Errorf("wallet withdraw: source wallet %s has zero balance", nodeWallet.String())
	}
	destBefore, err := fetchAccountSnapshot(ctx, api, to)
	if err != nil {
		return nil, fmt.Errorf("wallet withdraw: destination balance: %w", err)
	}
	seqno, deployed, err := queryWalletSeqno(ctx, api, nodeWallet)
	if err != nil {
		return nil, fmt.Errorf("wallet withdraw: seqno: %w", err)
	}
	signed, err := cocoonwallet.CreateSignedExternalMessage(
		[]cocoonwallet.OutboundMessage{{
			To:      to,
			Value:   0,
			Mode:    walletWithdrawMode,
			ModeSet: true,
			Bounce:  false,
		}},
		nodeKey,
		cocoonwallet.SignedExternalMessageOpts{Seqno: seqno},
	)
	if err != nil {
		return nil, fmt.Errorf("wallet withdraw: build ext-msg: %w", err)
	}
	extMsg := &tlb.ExternalMessage{
		DstAddr: nodeWallet,
		Body:    signed,
	}
	if !deployed {
		if code == nil {
			return nil, errors.New("wallet withdraw: wallet not deployed and wallet code is nil")
		}
		data, err := cocoonwallet.EncodeStorage(cocoonwallet.Config{
			PublicKey:    nodeKey.Public().(ed25519.PublicKey),
			OwnerAddress: owner,
		})
		if err != nil {
			return nil, fmt.Errorf("wallet withdraw: build state init data: %w", err)
		}
		extMsg.StateInit = &tlb.StateInit{
			Code: code,
			Data: data,
		}
	}
	msgHash := signed.Hash()
	out := &walletWithdrawOutput{
		FromAddress:   nodeWallet.String(),
		ToAddress:     to.String(),
		Mode:          walletWithdrawMode,
		Seqno:         seqno,
		WasDeployed:   deployed,
		BalanceNano:   sourceBefore.Balance.String(),
		BalanceTON:    formatNanoTON(sourceBefore.Balance),
		ExternalHash:  hex.EncodeToString(msgHash),
		InMessageHash: hex.EncodeToString(msgHash),
	}
	if err := api.SendExternalMessage(ctx, extMsg); err != nil {
		return nil, fmt.Errorf("wallet withdraw: send external message: %w", err)
	}
	confirmation, err := waitWalletWithdrawConfirmation(ctx, api, nodeWallet, to, sourceBefore, destBefore, poll)
	if err != nil {
		return out, err
	}
	out.Confirmed = true
	out.Confirmation = confirmation
	return out, nil
}

func queryWalletSeqno(ctx context.Context, api ton.APIClientWrapped, walletAddr *address.Address) (uint32, bool, error) {
	mc, err := api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return 0, false, err
	}
	res, err := api.RunGetMethod(ctx, mc, walletAddr, "seqno")
	if err != nil {
		return 0, false, nil //nolint:nilerr // uninitialized wallets start at seqno 0
	}
	v, err := res.Int(0)
	if err != nil {
		return 0, true, fmt.Errorf("parse seqno: %w", err)
	}
	if !v.IsUint64() {
		return 0, true, errors.New("seqno overflow")
	}
	return uint32(v.Uint64()), true, nil
}

type accountSnapshot struct {
	Active   bool
	Status   string
	Balance  *big.Int
	LastTxLT uint64
}

func fetchAccountSnapshot(ctx context.Context, api ton.APIClientWrapped, addr *address.Address) (*accountSnapshot, error) {
	mc, err := api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := api.GetAccount(ctx, mc, addr)
	if err != nil {
		return nil, err
	}
	out := &accountSnapshot{Balance: big.NewInt(0)}
	if acc == nil || acc.State == nil {
		return out, nil
	}
	out.Active = acc.IsActive && acc.State.Status == tlb.AccountStatusActive
	out.Status = string(acc.State.Status)
	out.LastTxLT = acc.LastTxLT
	if acc.State.Balance.Nano() != nil {
		out.Balance = new(big.Int).Set(acc.State.Balance.Nano())
	}
	return out, nil
}

func waitWalletWithdrawConfirmation(ctx context.Context, api ton.APIClientWrapped, source, dest *address.Address, sourceBefore, destBefore *accountSnapshot, poll time.Duration) (string, error) {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		sourceNow, sourceErr := fetchAccountSnapshot(ctx, api, source)
		destNow, destErr := fetchAccountSnapshot(ctx, api, dest)
		if sourceErr == nil && sourceNow.walletDrainedFrom(sourceBefore) {
			return "source wallet is inactive or emptied", nil
		}
		if sourceErr == nil && destErr == nil &&
			sourceNow.Balance.Cmp(sourceBefore.Balance) < 0 &&
			destNow.Balance.Cmp(destBefore.Balance) > 0 {
			return "source balance decreased and destination balance increased", nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("wallet withdraw: sent external message but confirmation was not observed before timeout: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *accountSnapshot) walletDrainedFrom(before *accountSnapshot) bool {
	if s == nil {
		return false
	}
	if before != nil && s.LastTxLT != 0 && before.LastTxLT != 0 && s.LastTxLT <= before.LastTxLT {
		return false
	}
	if !s.Active {
		return true
	}
	return s.Balance.Sign() == 0
}

func resolveTONConfigPath(configPath, tonConfigPath string) (string, error) {
	if tonConfigPath != "" {
		return filepath.Abs(tonConfigPath)
	}
	if configPath == "" {
		return "", nil
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("config: read %s: %w", configPath, err)
	}
	var cfg runnerConfigJSON
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("config: parse %s: %w", configPath, err)
	}
	if cfg.TonConfigFilename == "" {
		return "", fmt.Errorf("config: %s missing ton_config_filename", configPath)
	}
	if filepath.IsAbs(cfg.TonConfigFilename) {
		return cfg.TonConfigFilename, nil
	}
	return filepath.Abs(filepath.Join(filepath.Dir(configPath), cfg.TonConfigFilename))
}

func fetchFundingStatus(ctx context.Context, tonConfigPath, nodeAddress string) (*fundingStatus, error) {
	api, err := newTONAPI(tonConfigPath)
	if err != nil {
		return nil, err
	}
	addr, err := address.ParseAddr(nodeAddress)
	if err != nil {
		return nil, fmt.Errorf("wallet: parse node address: %w", err)
	}
	mc, err := api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("wallet: masterchain info: %w", err)
	}
	acc, err := api.GetAccount(ctx, mc, addr)
	if err != nil {
		return nil, fmt.Errorf("wallet: get account: %w", err)
	}
	bal := big.NewInt(0)
	if acc != nil && acc.State != nil && acc.State.Balance.Nano() != nil {
		bal = new(big.Int).Set(acc.State.Balance.Nano())
	}
	status := &fundingStatus{BalanceNano: bal}
	rdr, err := root.NewReader(api, defaultMainnetRoot)
	if err == nil {
		if summary, err := rdr.GetSummary(ctx); err == nil && summary.MinClientStake != nil {
			status.MinClientStakeNano = new(big.Int).Set(summary.MinClientStake)
		}
	}
	return status, nil
}

func newTONAPI(tonConfigPath string) (ton.APIClientWrapped, error) {
	pool := liteclient.NewConnectionPool()
	if err := pool.AddConnectionsFromConfigFile(tonConfigPath); err != nil {
		return nil, fmt.Errorf("liteclient: %w", err)
	}
	return ton.NewAPIClient(pool).WithRetry(3), nil
}

func printWalletInfo(out io.Writer, info walletInfoOutput) {
	fmt.Fprintln(out, "wallet:", info.WalletPath)
	fmt.Fprintln(out, "owner_address:", info.OwnerAddress)
	fmt.Fprintln(out, "fund_address:", info.FundAddress)
	fmt.Fprintln(out, "recommended_funding:", info.RecommendedFundingTON, "TON")
	fmt.Fprintln(out, "recommended_funding_nano:", info.RecommendedFundingNano)
	if info.BalanceNano != "" {
		fmt.Fprintln(out, "balance:", info.BalanceTON, "TON")
		fmt.Fprintln(out, "balance_nano:", info.BalanceNano)
		if info.Funded != nil {
			fmt.Fprintln(out, "funded:", *info.Funded)
		}
	}
	if info.OnChainMinClientStakeNano != "" {
		fmt.Fprintln(out, "on_chain_min_client_stake:", info.OnChainMinClientStakeTON, "TON")
		fmt.Fprintln(out, "on_chain_min_client_stake_nano:", info.OnChainMinClientStakeNano)
	}
}

func printWalletWithdraw(out io.Writer, info *walletWithdrawOutput) {
	fmt.Fprintln(out, "wallet:", info.WalletPath)
	fmt.Fprintln(out, "from:", info.FromAddress)
	fmt.Fprintln(out, "to:", info.ToAddress)
	fmt.Fprintln(out, "mode:", info.Mode, "(carry all remaining balance, destroy if zero)")
	fmt.Fprintln(out, "seqno:", info.Seqno)
	fmt.Fprintln(out, "was_deployed:", info.WasDeployed)
	fmt.Fprintln(out, "balance_before:", info.BalanceTON, "TON")
	fmt.Fprintln(out, "balance_before_nano:", info.BalanceNano)
	fmt.Fprintln(out, "external_hash:", info.ExternalHash)
	fmt.Fprintln(out, "in_message_hash:", info.InMessageHash)
	fmt.Fprintln(out, "confirmed:", info.Confirmed)
	if info.Confirmation != "" {
		fmt.Fprintln(out, "confirmation:", info.Confirmation)
	}
	if info.TransactionLT != 0 {
		fmt.Fprintln(out, "transaction_lt:", info.TransactionLT)
	}
	if info.TransactionHex != "" {
		fmt.Fprintln(out, "transaction_hash:", info.TransactionHex)
	}
}

func formatNanoTON(n *big.Int) string {
	if n == nil {
		return "0"
	}
	sign := ""
	v := new(big.Int).Set(n)
	if v.Sign() < 0 {
		sign = "-"
		v.Abs(v)
	}
	div := big.NewInt(1_000_000_000)
	whole, frac := new(big.Int).QuoRem(v, div, new(big.Int))
	if frac.Sign() == 0 {
		return sign + whole.String()
	}
	fracStr := fmt.Sprintf("%09s", frac.String())
	fracStr = strings.TrimRight(fracStr, "0")
	return sign + whole.String() + "." + fracStr
}

func printWalletUsage(out *os.File) {
	fmt.Fprintln(out, "gocoon wallet — Cocoon wallet generation and inspection")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  gocoon wallet generate [--wallet-code path] [--pretty]")
	fmt.Fprintln(out, "  gocoon wallet info --wallet ./gocoon-data/wallet.json [--config ./gocoon-data/client-config.json]")
	fmt.Fprintln(out, "  gocoon wallet wait-funded --wallet ./gocoon-data/wallet.json --config ./gocoon-data/client-config.json")
	fmt.Fprintln(out, "  gocoon wallet withdraw --wallet ./gocoon-data/wallet.json --config ./gocoon-data/client-config.json --to <address>")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  generate   Generate a complete owner/node Cocoon wallet bundle as JSON")
	fmt.Fprintln(out, "  info       Print fund address, recommended amount, and optional on-chain balance")
	fmt.Fprintln(out, "  wait-funded Poll TON until the Cocoon wallet has the required balance")
	fmt.Fprintln(out, "  withdraw   Send all remaining TON from the Cocoon wallet to a destination")
	fmt.Fprintln(out, "  drain      Alias for withdraw")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "The generated JSON contains secrets: ownerMnemonic and nodeSecretBase64.")
}

func cmdChat(args []string) error {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	baseURL := fs.String("url", defaultRunnerURL, "runner base URL")
	model := fs.String("model", "default", "model name, or default")
	prompt := fs.String("prompt", "", "user prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *prompt == "" && fs.NArg() > 0 {
		*prompt = strings.Join(fs.Args(), " ")
	}
	if *prompt == "" {
		return errors.New("chat: prompt is required")
	}
	req := map[string]any{
		"model":  *model,
		"stream": false,
		"messages": []map[string]string{{
			"role":    "user",
			"content": *prompt,
		}},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := http.Post(strings.TrimRight(*baseURL, "/")+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("chat: runner returned %s: %s", resp.Status, strings.TrimSpace(string(out)))
	}
	_, err = os.Stdout.Write(out)
	if err == nil && len(out) > 0 && out[len(out)-1] != '\n' {
		fmt.Println()
	}
	return err
}

func cmdServe(args []string) error {
	return cmdRun(args)
}

func cmdDoctor(_ []string) error {
	fmt.Printf("gocoon-doctor: version=%s commit=%s\n", cocoon.Version, cocoon.Commit)
	fmt.Println("gocoon-doctor: ok (basic build healthy)")
	return nil
}

func getJSONToStdout(url string) error {
	body, err := getURL(url)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, body, "", "  "); err == nil {
		pretty.WriteByte('\n')
		_, err = pretty.WriteTo(os.Stdout)
		return err
	}
	_, err = os.Stdout.Write(body)
	return err
}

func getURL(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: %s: %s", url, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}
