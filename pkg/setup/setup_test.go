package setup

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateFreshBundle(t *testing.T) {
	dir := t.TempDir()
	backup, err := Create(dir, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(backup.OwnerMnemonic) != 24 {
		t.Fatalf("mnemonic words = %d, want 24", len(backup.OwnerMnemonic))
	}
	if backup.OwnerAddress == "" || backup.FundAddress == "" {
		t.Fatalf("addresses missing: %+v", backup)
	}
	paths := DefaultPaths(dir)
	for _, p := range []string{paths.WalletPath, paths.ConfigPath, paths.TONConfigPath} {
		if !FileExists(p) {
			t.Fatalf("missing output file %s", p)
		}
	}

	var cfg RunnerConfig
	raw, err := os.ReadFile(paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPPort != 10000 || cfg.ProxyConnections != 1 {
		t.Fatalf("config defaults wrong: %+v", cfg)
	}
	if cfg.OwnerAddress != backup.OwnerAddress {
		t.Fatalf("config owner %q != backup owner %q", cfg.OwnerAddress, backup.OwnerAddress)
	}
	if cfg.RootContractAddr != MainnetRoot {
		t.Fatalf("root = %q", cfg.RootContractAddr)
	}
	if !filepath.IsAbs(cfg.TonConfigFilename) {
		t.Fatalf("ton_config_filename not absolute: %q", cfg.TonConfigFilename)
	}

	// Second create without force must refuse.
	if _, err := Create(dir, CreateOptions{}); err == nil {
		t.Fatal("expected error on existing files")
	}
}

func TestCreateFromMnemonicKeepsOwner(t *testing.T) {
	dir1 := t.TempDir()
	first, err := Create(dir1, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	dir2 := t.TempDir()
	second, err := Create(dir2, CreateOptions{OwnerMnemonic: first.OwnerMnemonic})
	if err != nil {
		t.Fatal(err)
	}
	if second.OwnerAddress != first.OwnerAddress {
		t.Fatalf("owner mismatch: %q vs %q", second.OwnerAddress, first.OwnerAddress)
	}
	// Node key is regenerated on mnemonic import → fund address differs.
	if second.FundAddress == first.FundAddress {
		t.Fatal("fund address should differ for a regenerated node key")
	}
}

func TestCreateFromBackupRestoresExactly(t *testing.T) {
	dir1 := t.TempDir()
	first, err := Create(dir1, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	dir2 := t.TempDir()
	restored, err := Create(dir2, CreateOptions{WalletJSON: []byte(first.BackupJSON)})
	if err != nil {
		t.Fatal(err)
	}
	if restored.OwnerAddress != first.OwnerAddress {
		t.Fatalf("owner mismatch: %q vs %q", restored.OwnerAddress, first.OwnerAddress)
	}
	if restored.FundAddress != first.FundAddress {
		t.Fatalf("fund mismatch: %q vs %q", restored.FundAddress, first.FundAddress)
	}
	if restored.NodeSecretBase64 != first.NodeSecretBase64 {
		t.Fatal("node secret mismatch")
	}

	info, err := LoadWalletInfo(DefaultPaths(dir2).WalletPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.NodeAddress != first.FundAddress {
		t.Fatalf("derived node address %q != %q", info.NodeAddress, first.FundAddress)
	}
}

func TestReadBackupRoundtrip(t *testing.T) {
	dir := t.TempDir()
	created, err := Create(dir, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadBackup(DefaultPaths(dir).WalletPath)
	if err != nil {
		t.Fatal(err)
	}
	if read.OwnerMnemonicText != created.OwnerMnemonicText {
		t.Fatal("mnemonic mismatch")
	}
	if read.FundAddress != created.FundAddress {
		t.Fatal("fund address mismatch")
	}
}

func TestNormalizeMnemonic(t *testing.T) {
	if _, err := NormalizeMnemonic("one two three"); err == nil {
		t.Fatal("expected error for short mnemonic")
	}
	words, err := NormalizeMnemonic("  A b c d e f g h i j k l m n o p q r s t u v w x  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(words) != 24 || words[0] != "a" {
		t.Fatalf("words = %#v", words)
	}
}

func TestFormatNanoTON(t *testing.T) {
	cases := map[int64]string{
		0:              "0",
		1_000_000_000:  "1",
		1_500_000_000:  "1.5",
		1430:           "0.00000143",
		20_000_000_000: "20",
		-2_250_000_000: "-2.25",
	}
	for in, want := range cases {
		if got := FormatNanoTON(big.NewInt(in)); got != want {
			t.Errorf("FormatNanoTON(%d) = %q, want %q", in, got, want)
		}
	}
}
