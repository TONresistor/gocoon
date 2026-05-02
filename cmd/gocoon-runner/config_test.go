package main

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/TONresistor/gocoon/pkg/cocoon"
)

func writeConfig(t *testing.T, c ClientConfig) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "client-config.json")
	body := []byte(`{"is_test":` + boolJSON(bool(c.IsTest)) +
		`,"is_testnet":` + boolJSON(bool(c.IsTestnet)) +
		`,"http_port":` + intJSON(c.HTTPPort) +
		`,"proxy_connections":` + intJSON(c.ProxyConnections) +
		`,"owner_address":"` + c.OwnerAddress + `"` +
		`,"root_contract_address":"` + c.RootContractAddr + `"` +
		`,"node_wallet_key":"` + c.NodeWalletKey + `"` +
		`,"ton_config_filename":"` + c.TonConfigFilename + `"}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
func intJSON(i int) string {
	out := []byte{}
	if i == 0 {
		return "0"
	}
	for i > 0 {
		out = append([]byte{byte('0' + i%10)}, out...)
		i /= 10
	}
	return string(out)
}

func validKey() string {
	return base64.StdEncoding.EncodeToString(make([]byte, 32))
}

func TestConfigValidateHappy(t *testing.T) {
	c := ClientConfig{
		IsTest:            false,
		IsTestnet:         false,
		HTTPPort:          10000,
		ProxyConnections:  1,
		OwnerAddress:      MainnetRoot,
		RootContractAddr:  MainnetRoot,
		NodeWalletKey:     validKey(),
		TonConfigFilename: "global.config.json",
	}
	if err := c.Validate(); err != nil {
		t.Errorf("expected valid: %v", err)
	}
	if _, err := c.NodeKeyBytes(); err != nil {
		t.Errorf("NodeKeyBytes: %v", err)
	}
}

func TestConfigValidateRejectsTestnet(t *testing.T) {
	c := ClientConfig{
		IsTestnet:         true,
		HTTPPort:          10000,
		ProxyConnections:  1,
		OwnerAddress:      MainnetRoot,
		RootContractAddr:  MainnetRoot,
		NodeWalletKey:     validKey(),
		TonConfigFilename: "global.config.json",
	}
	err := c.Validate()
	if !errors.Is(err, cocoon.ErrInvalidConfig) {
		t.Errorf("got %v", err)
	}
}

func TestConfigValidateRejectsBadPort(t *testing.T) {
	c := ClientConfig{
		HTTPPort:          0,
		ProxyConnections:  1,
		OwnerAddress:      MainnetRoot,
		RootContractAddr:  MainnetRoot,
		NodeWalletKey:     validKey(),
		TonConfigFilename: "global.config.json",
	}
	if !errors.Is(c.Validate(), cocoon.ErrInvalidConfig) {
		t.Errorf("expected invalid")
	}
}

func TestConfigLoadFromFile(t *testing.T) {
	c := ClientConfig{
		HTTPPort:          10000,
		ProxyConnections:  1,
		OwnerAddress:      MainnetRoot,
		RootContractAddr:  MainnetRoot,
		NodeWalletKey:     validKey(),
		TonConfigFilename: "global.config.json",
	}
	path := writeConfig(t, c)
	loaded, err := LoadClientConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.HTTPPort != c.HTTPPort {
		t.Errorf("port mismatch")
	}
}

func TestResolveTONConfig(t *testing.T) {
	c := ClientConfig{TonConfigFilename: "global.config.json"}
	got := c.ResolveTONConfig("/tmp/run/abc/client-config.json")
	if got != "/tmp/run/abc/global.config.json" {
		t.Errorf("got %s", got)
	}
}
