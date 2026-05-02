package wallet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/TONresistor/gocoon/pkg/resources"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// DefaultCodePaths returns the standard lookup order for cocoon-wallet.code.json.
func DefaultCodePaths(extra ...string) []string {
	candidates := make([]string, 0, len(extra)+6)
	candidates = append(candidates, extra...)
	if env := os.Getenv("GOCOON_WALLET_CODE"); env != "" {
		candidates = append(candidates, env)
	}
	if rd := os.Getenv("GOCOON_RESOURCE_DIR"); rd != "" {
		candidates = append(candidates,
			filepath.Join(rd, "cocoon", "cocoon-wallet.code.json"),
			filepath.Join(rd, "cocoon-wallet.code.json"),
		)
	}
	return append(candidates,
		"./resources/cocoon/cocoon-wallet.code.json",
		"../Tonnet-Browser-stable/resources/cocoon/cocoon-wallet.code.json",
	)
}

// LocateCode loads the first valid cocoon-wallet.code.json from candidates.
func LocateCode(candidates ...string) (*cell.Cell, string, error) {
	for _, p := range candidates {
		if p == "" {
			continue
		}
		c, err := CodeFromBoc(p)
		if err == nil {
			return c, p, nil
		}
	}
	return nil, "", errors.New("could not locate cocoon-wallet.code.json (set GOCOON_WALLET_CODE or pass --wallet-code)")
}

// LoadDefaultCode loads cocoon-wallet.code.json from the default lookup order.
func LoadDefaultCode(extra ...string) (*cell.Cell, string, error) {
	code, path, err := LocateCode(DefaultCodePaths(extra...)...)
	if err != nil {
		code, embeddedErr := CodeFromBocBytes(resources.CocoonWalletCodeJSON)
		if embeddedErr == nil {
			return code, "<embedded:cocoon-wallet.code.json>", nil
		}
		return nil, "", fmt.Errorf("wallet code: %w", err)
	}
	return code, path, nil
}
