package wallet

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/xssnick/tonutils-go/tvm/cell"
)

func decodeHexBoc(s string) (*cell.Cell, error) {
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("wallet: hex decode: %w", err)
	}
	return cell.FromBOC(raw)
}

func decodeB64Boc(s string) (*cell.Cell, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// Try URL-safe encoding too.
		raw, err = base64.URLEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("wallet: base64 decode: %w", err)
		}
	}
	return cell.FromBOC(raw)
}
