package core

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/TONresistor/gocoon/pkg/cocoon"
	"github.com/xssnick/tonutils-go/address"
)

// MainnetRoot is the cocoon_root contract address on TON mainnet.
const MainnetRoot = "EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k"

// flexBool accepts JSON `true`, `false`, `0`, `1`, `"0"`, `"1"`, `"true"`,
// `"false"`. The browser's client-config template substitutes `$IS_DEBUG`
// with an integer (0 or 1) for is_test, so a strict bool decoder rejects
// the rendered file. flexBool keeps us compatible with any rendering.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case "true", "1", `"1"`, `"true"`:
		*b = true
		return nil
	case "false", "0", `"0"`, `"false"`, `""`, "null":
		*b = false
		return nil
	}
	return fmt.Errorf("flexBool: cannot parse %s", data)
}

// ClientConfig mirrors clientRunner.config from cocoon_api.tl plus a few
// browser-rendered fields (rpc_port, max_*) that we accept but don't all use.
type ClientConfig struct {
	IsTest            flexBool `json:"is_test"`
	IsTestnet         flexBool `json:"is_testnet"`
	HTTPPort          int      `json:"http_port"`
	RPCPort           int      `json:"rpc_port,omitempty"`
	ProxyConnections  int      `json:"proxy_connections"`
	ConnectToProxyVia string   `json:"connect_to_proxy_via"`
	OwnerAddress      string   `json:"owner_address"`
	RootContractAddr  string   `json:"root_contract_address"`
	NodeWalletKey     string   `json:"node_wallet_key"`
	CheckProxyHashes  flexBool `json:"check_proxy_hashes,omitempty"`
	SecretString      string   `json:"secret_string,omitempty"`
	TonConfigFilename string   `json:"ton_config_filename"`
	HTTPAccessHash    string   `json:"http_access_hash,omitempty"`
	MaxCoefficient    int      `json:"max_coefficient,omitempty"`
	MaxTokens         int      `json:"max_tokens,omitempty"`
}

// LoadClientConfig reads and validates a config file.
func LoadClientConfig(path string) (*ClientConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var cfg ClientConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks the runner config fields needed by the Go implementation.
func (c *ClientConfig) Validate() error {
	if c.HTTPPort < 1 || c.HTTPPort > 65535 {
		return &cocoon.ConfigError{Field: "http_port", Reason: "must be 1..65535"}
	}
	if c.ProxyConnections < 1 {
		return &cocoon.ConfigError{Field: "proxy_connections", Reason: "must be >= 1"}
	}
	if _, err := address.ParseAddr(c.OwnerAddress); err != nil {
		return &cocoon.ConfigError{Field: "owner_address", Reason: err.Error()}
	}
	if _, err := address.ParseAddr(c.RootContractAddr); err != nil {
		return &cocoon.ConfigError{Field: "root_contract_address", Reason: err.Error()}
	}
	if c.RootContractAddr != MainnetRoot {
		return &cocoon.ConfigError{Field: "root_contract_address", Reason: "v1 mainnet only: " + MainnetRoot}
	}
	if c.IsTest || c.IsTestnet {
		return &cocoon.ConfigError{Field: "is_test/is_testnet", Reason: "v1 mainnet only"}
	}
	if c.NodeWalletKey == "" {
		return &cocoon.ConfigError{Field: "node_wallet_key", Reason: "required"}
	}
	keyBytes, err := base64.StdEncoding.DecodeString(c.NodeWalletKey)
	if err != nil {
		// Try url-safe encoding too.
		if k2, e2 := base64.URLEncoding.DecodeString(c.NodeWalletKey); e2 == nil {
			keyBytes = k2
		} else {
			return &cocoon.ConfigError{Field: "node_wallet_key", Reason: "not valid base64"}
		}
	}
	if len(keyBytes) != 32 {
		return &cocoon.ConfigError{Field: "node_wallet_key", Reason: fmt.Sprintf("decoded length %d, want 32", len(keyBytes))}
	}
	if c.TonConfigFilename == "" {
		return &cocoon.ConfigError{Field: "ton_config_filename", Reason: "required"}
	}
	return nil
}

// NodeKeyBytes returns the decoded 32-byte Ed25519 secret seed.
func (c *ClientConfig) NodeKeyBytes() ([]byte, error) {
	keyBytes, err := base64.StdEncoding.DecodeString(c.NodeWalletKey)
	if err != nil {
		keyBytes, err = base64.URLEncoding.DecodeString(c.NodeWalletKey)
		if err != nil {
			return nil, errors.New("config: node_wallet_key not valid base64")
		}
	}
	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("config: node_wallet_key decoded length %d != 32", len(keyBytes))
	}
	return keyBytes, nil
}

// ResolveTONConfig returns the absolute path of the TON network config,
// resolving ton_config_filename relative to the config file directory.
func (c *ClientConfig) ResolveTONConfig(configFilePath string) string {
	if filepath.IsAbs(c.TonConfigFilename) {
		return c.TonConfigFilename
	}
	return filepath.Join(filepath.Dir(configFilePath), c.TonConfigFilename)
}
