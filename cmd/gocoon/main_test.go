package main

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

func TestKnownCommandsDispatchable(t *testing.T) {
	expected := []string{"version", "help", "status", "proxies", "root", "models", "init", "config", "run", "channel", "wallet", "chat", "serve", "doctor"}
	for _, name := range expected {
		if _, ok := commands[name]; !ok {
			t.Errorf("missing command: %s", name)
		}
	}
}

func TestInitWritesStandaloneBundle(t *testing.T) {
	dir := t.TempDir()
	if err := cmdInit([]string{"--dir", dir}); err != nil {
		t.Fatalf("init: %v", err)
	}

	walletPath := filepath.Join(dir, "wallet.json")
	tonConfigPath := filepath.Join(dir, "ton-config.json")
	configPath := filepath.Join(dir, "client-config.json")
	for _, path := range []string{walletPath, tonConfigPath, configPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s: %v", path, err)
		}
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg runnerConfigJSON
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPPort != 10000 || cfg.RPCPort != 10001 {
		t.Fatalf("ports: %+v", cfg)
	}
	if cfg.OwnerAddress == "" || cfg.NodeWalletKey == "" || cfg.TonConfigFilename == "" {
		t.Fatalf("missing required fields: %+v", cfg)
	}
	if !filepath.IsAbs(cfg.TonConfigFilename) {
		t.Fatalf("ton config path should be absolute: %q", cfg.TonConfigFilename)
	}
	info, err := loadWalletInfo(walletPath)
	if err != nil {
		t.Fatalf("wallet info: %v", err)
	}
	if info.OwnerAddress == "" || info.NodeAddress == "" {
		t.Fatalf("missing wallet info: %+v", info)
	}

	if err := cmdInit([]string{"--dir", dir}); err == nil {
		t.Fatalf("second init without --force should fail")
	}
	if err := cmdInit([]string{"--dir", dir, "--force"}); err != nil {
		t.Fatalf("init --force: %v", err)
	}
}

func TestVersionAndDoctorSucceed(t *testing.T) {
	if err := cmdVersion(nil); err != nil {
		t.Errorf("version: %v", err)
	}
	if err := cmdDoctor(nil); err != nil {
		t.Errorf("doctor: %v", err)
	}
}

func TestChannelRequiresSubVerb(t *testing.T) {
	if err := cmdChannel(nil); err != nil {
		t.Errorf("no-arg channel should print and exit ok: %v", err)
	}
	if err := cmdChannel([]string{"bogus"}); err == nil {
		t.Errorf("expected error on bogus verb")
	}
	if err := cmdChannel([]string{"topup", "--client-sc", "EQbad", "--url", "http://127.0.0.1:1"}); err == nil {
		t.Errorf("topup should fail when runner is unreachable")
	}
}

func TestRootRequiresTONConfig(t *testing.T) {
	if err := cmdRoot(nil); err == nil {
		t.Errorf("root should require --config or --ton-config")
	}
}

func TestWalletRequiresKnownSubVerb(t *testing.T) {
	if err := cmdWallet(nil); err != nil {
		t.Errorf("no-arg wallet should print and exit ok: %v", err)
	}
	if err := cmdWallet([]string{"bogus"}); err == nil {
		t.Errorf("expected error on bogus verb")
	}
	if err := cmdWallet([]string{"withdraw"}); err == nil {
		t.Errorf("withdraw should require --to")
	}
	if err := cmdWallet([]string{"drain"}); err == nil {
		t.Errorf("drain should require --to")
	}
}

func TestConfigGenerateWritesRunnerConfig(t *testing.T) {
	dir := t.TempDir()
	walletPath := filepath.Join(dir, "wallet.json")
	tonConfigPath := filepath.Join(dir, "global.config.json")
	outPath := filepath.Join(dir, "client-config.json")
	walletJSON := `{
	  "ownerAddress": "EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k",
	  "nodeSecretBase64": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	}`
	if err := os.WriteFile(walletPath, []byte(walletJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tonConfigPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := cmdConfigGenerate([]string{
		"--wallet", walletPath,
		"--ton-config", tonConfigPath,
		"--out", outPath,
		"--http-port", "10010",
		"--rpc-port", "10011",
	})
	if err != nil {
		t.Fatalf("config generate: %v", err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg runnerConfigJSON
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPPort != 10010 || cfg.RPCPort != 10011 {
		t.Fatalf("ports: %+v", cfg)
	}
	if cfg.OwnerAddress == "" || cfg.NodeWalletKey == "" || cfg.TonConfigFilename == "" {
		t.Fatalf("missing required fields: %+v", cfg)
	}
	if !filepath.IsAbs(cfg.TonConfigFilename) {
		t.Fatalf("ton config path should be absolute: %q", cfg.TonConfigFilename)
	}
}

func TestFormatNanoTON(t *testing.T) {
	tests := map[string]string{
		"0":           "0",
		"1":           "0.000000001",
		"1000000000":  "1",
		"1500000000":  "1.5",
		"20000000000": "20",
	}
	for in, want := range tests {
		got := formatNanoTON(mustBigInt(t, in))
		if got != want {
			t.Fatalf("formatNanoTON(%s) = %s, want %s", in, got, want)
		}
	}
}

func mustBigInt(t *testing.T, s string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad big int literal %q", s)
	}
	return n
}
