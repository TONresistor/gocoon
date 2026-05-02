package wallet

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// Config is the cocoon_wallet storage config used at deploy time.
type Config struct {
	PublicKey    ed25519.PublicKey
	OwnerAddress *address.Address
}

// EncodeStorage builds the initial storage cell for a cocoon_wallet SC.
//
//	storage = seqno (int 32) | subwallet (int 32) | public_key (256 bits)
//	        | status (uint 32) | owner_address (Address)
//
// Source of layout: TelegramMessenger/cocoon-contracts/wrappers/CocoonWallet.ts:cocoonWalletConfigToCell.
func EncodeStorage(cfg Config) (*cell.Cell, error) {
	if len(cfg.PublicKey) != 32 {
		return nil, fmt.Errorf("wallet: public key must be 32 bytes, got %d", len(cfg.PublicKey))
	}
	if cfg.OwnerAddress == nil {
		return nil, errors.New("wallet: owner address required")
	}
	return cell.BeginCell().
		MustStoreInt(0, 32).                // seqno
		MustStoreInt(0, 32).                // subwallet
		MustStoreSlice(cfg.PublicKey, 256). // pubkey
		MustStoreUInt(0, 32).               // status
		MustStoreAddr(cfg.OwnerAddress).
		EndCell(), nil
}

// CodeFromBoc loads the bundled cocoon_wallet code BoC from a JSON file
// matching the browser's resources/cocoon/cocoon-wallet.code.json shape.
//
// File schema (compatible with @ton/blueprint compile output):
//
//	{
//	  "hex":  "<hex string of the BoC>",
//	  "boc":  "<base64 ditto>"
//	}
//
// Either field is acceptable; if both are present, "hex" wins.
func CodeFromBoc(path string) (*cell.Cell, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("wallet: read code file: %w", err)
	}
	return CodeFromBocBytes(raw)
}

// CodeFromBocBytes parses a code-json blob.
func CodeFromBocBytes(raw []byte) (*cell.Cell, error) {
	var v struct {
		Hex string `json:"hex"`
		BOC string `json:"boc"`
		// Some Blueprint outputs use "hash" + "code" fields:
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("wallet: parse code json: %w", err)
	}
	switch {
	case v.Hex != "":
		return decodeHexBoc(v.Hex)
	case v.BOC != "":
		return decodeB64Boc(v.BOC)
	case v.Code != "":
		return decodeB64Boc(v.Code)
	}
	return nil, errors.New("wallet: code json missing hex/boc/code field")
}

// DeriveAddress returns the cocoon_wallet address for the given config and code,
// using workchain 0.
func DeriveAddress(cfg Config, code *cell.Cell) (*address.Address, error) {
	data, err := EncodeStorage(cfg)
	if err != nil {
		return nil, err
	}
	if code == nil {
		return nil, errors.New("wallet: code is nil")
	}
	stateInit := cell.BeginCell().
		MustStoreUInt(0, 2). // split_depth=0, special=0 (both refs absent)
		MustStoreUInt(1, 1). // code present
		MustStoreRef(code).
		MustStoreUInt(1, 1). // data present
		MustStoreRef(data).
		MustStoreUInt(0, 1). // library absent
		EndCell()
	hash := stateInit.Hash()
	var raw [32]byte
	copy(raw[:], hash)
	return address.NewAddress(0, 0, raw[:]), nil
}

// OutboundMessage is the same shape as CocoonWallet.OutboundMessage in TS.
type OutboundMessage struct {
	To      *address.Address
	Value   uint64
	Body    *cell.Cell
	Init    *cell.Cell // optional StateInit cell, stored by ref
	Mode    uint8
	ModeSet bool // false = default 1 (PAY_GAS_SEPARATELY)
	Bounce  bool // default true
}

// BuildOutboundMessageCell mirrors CocoonWallet.buildOutboundMessageCell.
func BuildOutboundMessageCell(m OutboundMessage) (*cell.Cell, error) {
	if m.To == nil {
		return nil, errors.New("wallet: outbound: nil to")
	}
	flag := uint64(0x10)
	if m.Bounce {
		flag = 0x18
	}
	if m.Init != nil {
		// Upstream CocoonWallet uses non-bounceable internal messages when
		// deploying a smart contract via init:(Maybe ^StateInit).
		flag = 0x10
	}
	b := cell.BeginCell().
		MustStoreUInt(flag, 6).
		MustStoreAddr(m.To).
		MustStoreCoins(m.Value).
		MustStoreUInt(0, 1+4+4+64+32)
	if m.Init != nil {
		b = b.MustStoreUInt(1, 1).MustStoreUInt(1, 1).MustStoreRef(m.Init)
	} else {
		b = b.MustStoreUInt(0, 1)
	}
	if m.Body != nil {
		b = b.MustStoreUInt(1, 1).MustStoreRef(m.Body)
	} else {
		b = b.MustStoreUInt(0, 1)
	}
	return b.EndCell(), nil
}

// SignedExternalMessageOpts collects the optional inputs for CreateSignedExternalMessage.
type SignedExternalMessageOpts struct {
	Seqno       uint32
	SubwalletID uint32
	ValidUntil  uint32 // unix seconds; 0 = now+1h
}

// CreateSignedExternalMessage builds the signed body for a cocoon_wallet
// external message. Mirrors CocoonWallet.createExternalMessage in TS.
//
// Body layout BEFORE signing:
//
//	subwalletId (uint32) | validUntil (uint32) | seqno (uint32)
//	[ mode (uint8) | ref(msg) ]*
//
// Final cell:
//
//	signature(64 bytes) | <body slice copied in>
func CreateSignedExternalMessage(messages []OutboundMessage, secretKey ed25519.PrivateKey, opts SignedExternalMessageOpts) (*cell.Cell, error) {
	if len(secretKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("wallet: secret key must be %d bytes", ed25519.PrivateKeySize)
	}
	validUntil := opts.ValidUntil
	if validUntil == 0 {
		validUntil = uint32(time.Now().Unix()) + 3600
	}

	body := cell.BeginCell().
		MustStoreUInt(uint64(opts.SubwalletID), 32).
		MustStoreUInt(uint64(validUntil), 32).
		MustStoreUInt(uint64(opts.Seqno), 32)
	for _, m := range messages {
		mode := m.Mode
		if !m.ModeSet {
			mode = 1 // PAY_GAS_SEPARATELY
		}
		msgCell, err := BuildOutboundMessageCell(m)
		if err != nil {
			return nil, err
		}
		body = body.MustStoreUInt(uint64(mode), 8).MustStoreRef(msgCell)
	}
	bodyCell := body.EndCell()
	sig := ed25519.Sign(secretKey, bodyCell.Hash())

	// Equivalent to TS:
	//   beginCell().storeBuffer(signature).storeSlice(bodyCell.beginParse()).endCell()
	// In tonutils-go: write the signature, then copy the entire body builder
	// (bits + refs) via StoreBuilder.
	final := cell.BeginCell().
		MustStoreSlice(sig, 64*8).
		MustStoreBuilder(bodyCell.BeginParse().ToBuilder()).
		EndCell()
	return final, nil
}
