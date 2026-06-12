// Package setup owns the local Cocoon data directory: wallet bundle
// generation and import, runner config rendering, backups, and on-chain
// funding checks. It is the single implementation behind `gocoon init`,
// `gocoon ui`, and the runner's app API.
package setup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/ton"

	"github.com/TONresistor/gocoon/pkg/contracts/root"
	cocoonwallet "github.com/TONresistor/gocoon/pkg/contracts/wallet"
	"github.com/TONresistor/gocoon/pkg/resources"
)

const (
	// MainnetRoot is the cocoon_root contract address on TON mainnet.
	MainnetRoot = "EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k"
	// RecommendedFundingNano is the suggested initial funding of the Cocoon
	// node wallet (covers client SC deploy + min stake + fees).
	RecommendedFundingNano = uint64(20_000_000_000)
)

// Paths are the well-known files inside a Cocoon data directory.
type Paths struct {
	DataDir         string `json:"data_dir"`
	WalletPath      string `json:"wallet_path"`
	ConfigPath      string `json:"config_path"`
	TONConfigPath   string `json:"ton_config_path"`
	RunnerStatePath string `json:"runner_state_path"`
}

// DefaultPaths lays out the standard file names inside dataDir.
func DefaultPaths(dataDir string) Paths {
	return Paths{
		DataDir:         dataDir,
		WalletPath:      filepath.Join(dataDir, "wallet.json"),
		ConfigPath:      filepath.Join(dataDir, "client-config.json"),
		TONConfigPath:   filepath.Join(dataDir, "ton-config.json"),
		RunnerStatePath: filepath.Join(dataDir, "runner-state.bolt"),
	}
}

// RunnerConfig is the client-config.json shape consumed by gocoon-runner
// (and upstream cocoon-runner).
type RunnerConfig struct {
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

// Backup is everything the user needs to restore access to their funds.
// JSON field names match the web/desktop UI payload.
type Backup struct {
	WalletPath        string   `json:"wallet_path"`
	OwnerMnemonic     []string `json:"owner_mnemonic"`
	OwnerMnemonicText string   `json:"owner_mnemonic_text"`
	NodeSecretBase64  string   `json:"node_secret_base64"`
	OwnerAddress      string   `json:"owner_address"`
	FundAddress       string   `json:"fund_address"`
	BackupJSON        string   `json:"backup_json"`
}

// CreateOptions configures Create.
type CreateOptions struct {
	// HTTPPort / RPCPort go into client-config.json. Defaults: 10000 / 10001.
	HTTPPort int
	RPCPort  int
	// Force overwrites existing files instead of failing.
	Force bool
	// OwnerMnemonic imports an existing 24-word owner mnemonic. A fresh node
	// key is generated, so the fund address will differ from the original
	// bundle; use WalletJSON to restore an exact bundle.
	OwnerMnemonic []string
	// WalletJSON restores a full wallet.json backup verbatim. Takes
	// precedence over OwnerMnemonic.
	WalletJSON []byte
	// RootContractAddr overrides the root contract. Default MainnetRoot.
	RootContractAddr string
}

// Create writes wallet.json, ton-config.json, and client-config.json into
// dataDir and returns the backup bundle. Fails if any output exists unless
// opts.Force is set.
func Create(dataDir string, opts CreateOptions) (*Backup, error) {
	if opts.HTTPPort == 0 {
		opts.HTTPPort = 10000
	}
	if opts.RPCPort == 0 {
		opts.RPCPort = 10001
	}
	if opts.RootContractAddr == "" {
		opts.RootContractAddr = MainnetRoot
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("setup: create dir: %w", err)
	}
	paths := DefaultPaths(dataDir)
	for _, path := range []string{paths.WalletPath, paths.ConfigPath, paths.TONConfigPath} {
		if err := PreflightOutputFile(path, opts.Force); err != nil {
			return nil, err
		}
	}

	generated, walletJSON, err := buildWalletBundle(opts)
	if err != nil {
		return nil, err
	}
	if err := WriteOutputFile(paths.WalletPath, walletJSON, 0o600, opts.Force); err != nil {
		return nil, err
	}
	if err := WriteOutputFile(paths.TONConfigPath, resources.TONConfigJSON, 0o600, opts.Force); err != nil {
		return nil, err
	}
	absTonConfig, err := filepath.Abs(paths.TONConfigPath)
	if err != nil {
		return nil, err
	}
	cfg := RunnerConfig{
		IsTest:            false,
		IsTestnet:         false,
		HTTPPort:          opts.HTTPPort,
		RPCPort:           opts.RPCPort,
		ProxyConnections:  1,
		TonConfigFilename: absTonConfig,
		OwnerAddress:      generated.OwnerAddress,
		RootContractAddr:  opts.RootContractAddr,
		NodeWalletKey:     generated.NodeSecretBase64,
		MaxCoefficient:    0,
		MaxTokens:         0,
	}
	configJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	configJSON = append(configJSON, '\n')
	if err := WriteOutputFile(paths.ConfigPath, configJSON, 0o600, opts.Force); err != nil {
		return nil, err
	}
	return newBackup(paths.WalletPath, generated, walletJSON), nil
}

// buildWalletBundle produces the wallet bundle from opts: restored from a
// backup, derived from an imported mnemonic, or freshly generated.
func buildWalletBundle(opts CreateOptions) (*cocoonwallet.Generated, []byte, error) {
	if len(opts.WalletJSON) > 0 {
		var generated cocoonwallet.Generated
		if err := json.Unmarshal(opts.WalletJSON, &generated); err != nil {
			return nil, nil, fmt.Errorf("setup: parse backup: %w", err)
		}
		if generated.OwnerAddress == "" || generated.NodeSecretBase64 == "" {
			return nil, nil, errors.New("setup: backup missing ownerAddress or nodeSecretBase64")
		}
		if _, err := DecodeNodeSeed(generated.NodeSecretBase64); err != nil {
			return nil, nil, err
		}
		if generated.NodeAddress == "" {
			derived, err := deriveNodeAddress(generated.OwnerAddress, generated.NodeSecretBase64)
			if err != nil {
				return nil, nil, err
			}
			generated.NodeAddress = derived
		}
		walletJSON, err := json.MarshalIndent(&generated, "", "  ")
		if err != nil {
			return nil, nil, err
		}
		return &generated, append(walletJSON, '\n'), nil
	}

	code, _, err := cocoonwallet.LoadDefaultCode()
	if err != nil {
		return nil, nil, err
	}
	generated, err := cocoonwallet.Generate(cocoonwallet.GenerateOptions{
		Code:          code,
		OwnerMnemonic: opts.OwnerMnemonic,
	})
	if err != nil {
		return nil, nil, err
	}
	walletJSON, err := json.MarshalIndent(generated, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return generated, append(walletJSON, '\n'), nil
}

// NormalizeMnemonic splits free-form mnemonic input into words and validates
// the word count (24 for TON wallets).
func NormalizeMnemonic(input string) ([]string, error) {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(input)))
	if len(words) != 24 {
		return nil, fmt.Errorf("setup: mnemonic must be 24 words, got %d", len(words))
	}
	return words, nil
}

// ReadBackup loads the backup bundle from an existing wallet.json.
func ReadBackup(walletPath string) (*Backup, error) {
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
	backupJSON := raw
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err == nil {
		pretty.WriteByte('\n')
		backupJSON = pretty.Bytes()
	}
	return newBackup(walletPath, &generated, backupJSON), nil
}

func newBackup(walletPath string, generated *cocoonwallet.Generated, walletJSON []byte) *Backup {
	return &Backup{
		WalletPath:        walletPath,
		OwnerMnemonic:     append([]string(nil), generated.OwnerMnemonic...),
		OwnerMnemonicText: strings.Join(generated.OwnerMnemonic, " "),
		NodeSecretBase64:  generated.NodeSecretBase64,
		OwnerAddress:      generated.OwnerAddress,
		FundAddress:       generated.NodeAddress,
		BackupJSON:        string(walletJSON),
	}
}

// WalletInfo are the two addresses derived from a stored wallet.
type WalletInfo struct {
	OwnerAddress string
	NodeAddress  string
}

type storedWallet struct {
	OwnerAddress     string `json:"ownerAddress"`
	NodeSecretBase64 string `json:"nodeSecretBase64"`
	NodeAddress      string `json:"nodeAddress"`
}

// LoadWalletInfo reads wallet.json and derives the node (fund) address when
// the stored bundle predates the nodeAddress field.
func LoadWalletInfo(path string) (*WalletInfo, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("wallet: read %s: %w", path, err)
	}
	var wallet storedWallet
	if err := json.Unmarshal(raw, &wallet); err != nil {
		return nil, fmt.Errorf("wallet: parse %s: %w", path, err)
	}
	if wallet.OwnerAddress == "" || wallet.NodeSecretBase64 == "" {
		return nil, errors.New("wallet: JSON missing ownerAddress or nodeSecretBase64")
	}
	nodeAddress := strings.TrimSpace(wallet.NodeAddress)
	if nodeAddress == "" {
		nodeAddress, err = deriveNodeAddress(wallet.OwnerAddress, wallet.NodeSecretBase64)
		if err != nil {
			return nil, err
		}
	}
	return &WalletInfo{OwnerAddress: wallet.OwnerAddress, NodeAddress: nodeAddress}, nil
}

func deriveNodeAddress(ownerAddress, nodeSecretBase64 string) (string, error) {
	owner, err := address.ParseAddr(ownerAddress)
	if err != nil {
		return "", fmt.Errorf("wallet: parse ownerAddress: %w", err)
	}
	seed, err := DecodeNodeSeed(nodeSecretBase64)
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

// DecodeNodeSeed decodes the base64 node wallet seed (std or url-safe).
func DecodeNodeSeed(s string) ([]byte, error) {
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

// FundingStatus reports the node wallet balance and the on-chain minimum
// client stake (when readable).
type FundingStatus struct {
	BalanceNano        *big.Int
	MinClientStakeNano *big.Int
}

// FetchFundingStatus queries TON for the node wallet balance.
func FetchFundingStatus(ctx context.Context, tonConfigPath, nodeAddress string) (*FundingStatus, error) {
	api, err := NewTONAPI(tonConfigPath)
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
	status := &FundingStatus{BalanceNano: bal}
	rdr, err := root.NewReader(api, MainnetRoot)
	if err == nil {
		if summary, err := rdr.GetSummary(ctx); err == nil && summary.MinClientStake != nil {
			status.MinClientStakeNano = new(big.Int).Set(summary.MinClientStake)
		}
	}
	return status, nil
}

// NewTONAPI opens a liteclient connection pool from a TON global config file.
func NewTONAPI(tonConfigPath string) (ton.APIClientWrapped, error) {
	pool := liteclient.NewConnectionPool()
	if err := pool.AddConnectionsFromConfigFile(tonConfigPath); err != nil {
		return nil, fmt.Errorf("liteclient: %w", err)
	}
	return ton.NewAPIClient(pool).WithRetryTimeout(3, 0*time.Second), nil
}

// FormatNanoTON renders a nanoTON amount as a decimal TON string.
func FormatNanoTON(n *big.Int) string {
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

// WriteOutputFile writes data to path; refuses to overwrite unless force.
func WriteOutputFile(path string, data []byte, mode os.FileMode, force bool) error {
	if err := PreflightOutputFile(path, force); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// PreflightOutputFile fails when path exists and force is unset.
func PreflightOutputFile(path string, force bool) error {
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

// FileExists reports whether path exists and is a regular file.
func FileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// NonEmptyFileExists reports whether path exists and has content.
func NonEmptyFileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}
