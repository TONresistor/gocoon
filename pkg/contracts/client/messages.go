package client

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// Message bodies for cocoon_client SC.
//
// Each Build* function returns a *cell.Cell ready to be wrapped in an internal
// or external message. The query_id is set to 0 unless the caller passes a
// non-zero override (matches upstream wrappers/CocoonClient.ts which always
// uses 0 for owner-initiated ops).
//
// Source of byte layout: TelegramMessenger/cocoon-contracts/wrappers/CocoonClient.ts.

// BuildExtTopUp builds an ext_client_top_up body.
//
//	body = op:0xf172e6c2 (32) | query_id (64) | top_up_amount (Coins) | excesses_to (Address)
func BuildExtTopUp(topUpAmount *big.Int, sendExcessesTo *address.Address) (*cell.Cell, error) {
	if topUpAmount == nil || topUpAmount.Sign() < 0 {
		return nil, errors.New("client: BuildExtTopUp: invalid topUpAmount")
	}
	if sendExcessesTo == nil {
		return nil, errors.New("client: BuildExtTopUp: nil excessesTo")
	}
	return cell.BeginCell().
		MustStoreUInt(uint64(OpExtClientTopUp), 32).
		MustStoreUInt(0, 64). // query_id
		MustStoreBigCoins(topUpAmount).
		MustStoreAddr(sendExcessesTo).
		EndCell(), nil
}

// BuildOwnerChangeSecretHashAndTopUp builds the combined op body.
//
//	body = op:0x8473b408 | query_id | top_up_amount | new_secret_hash (256) | excesses_to
func BuildOwnerChangeSecretHashAndTopUp(topUpAmount *big.Int, newSecretHash *big.Int, sendExcessesTo *address.Address) (*cell.Cell, error) {
	if topUpAmount == nil || topUpAmount.Sign() < 0 {
		return nil, errors.New("client: invalid topUpAmount")
	}
	if newSecretHash == nil {
		return nil, errors.New("client: nil newSecretHash")
	}
	return cell.BeginCell().
		MustStoreUInt(uint64(OpOwnerClientChangeSecretHashAndTopUp), 32).
		MustStoreUInt(0, 64).
		MustStoreBigCoins(topUpAmount).
		MustStoreBigUInt(newSecretHash, 256).
		MustStoreAddr(sendExcessesTo).
		EndCell(), nil
}

// BuildOwnerRegister builds an owner_client_register body.
//
//	body = op:0xc45f9f3b | query_id | nonce (64) | excesses_to
func BuildOwnerRegister(nonce uint64, sendExcessesTo *address.Address) (*cell.Cell, error) {
	if sendExcessesTo == nil {
		return nil, errors.New("client: nil excessesTo")
	}
	var qid uint64
	if err := binary.Read(rand.Reader, binary.BigEndian, &qid); err != nil {
		return nil, fmt.Errorf("client: random query_id: %w", err)
	}
	if qid == 0 {
		qid = 1
	}
	return cell.BeginCell().
		MustStoreUInt(uint64(OpOwnerClientRegister), 32).
		MustStoreUInt(qid, 64).
		MustStoreUInt(nonce, 64).
		MustStoreAddr(sendExcessesTo).
		EndCell(), nil
}

// BuildOwnerChangeSecretHash builds the rotate-secret op body.
func BuildOwnerChangeSecretHash(newSecretHash *big.Int, sendExcessesTo *address.Address) (*cell.Cell, error) {
	if newSecretHash == nil {
		return nil, errors.New("client: nil newSecretHash")
	}
	return cell.BeginCell().
		MustStoreUInt(uint64(OpOwnerClientChangeSecretHash), 32).
		MustStoreUInt(0, 64).
		MustStoreBigUInt(newSecretHash, 256).
		MustStoreAddr(sendExcessesTo).
		EndCell(), nil
}

// BuildOwnerIncreaseStake builds the increase-stake op body.
func BuildOwnerIncreaseStake(newStake *big.Int, sendExcessesTo *address.Address) (*cell.Cell, error) {
	if newStake == nil || newStake.Sign() < 0 {
		return nil, errors.New("client: invalid newStake")
	}
	return cell.BeginCell().
		MustStoreUInt(uint64(OpOwnerClientIncreaseStake), 32).
		MustStoreUInt(0, 64).
		MustStoreBigCoins(newStake).
		MustStoreAddr(sendExcessesTo).
		EndCell(), nil
}

// BuildOwnerWithdraw builds the withdraw (skim balance > stake) op body.
//
//	body = op:0xda068e78 | query_id | excesses_to
func BuildOwnerWithdraw(sendExcessesTo *address.Address) (*cell.Cell, error) {
	if sendExcessesTo == nil {
		return nil, errors.New("client: nil excessesTo")
	}
	return cell.BeginCell().
		MustStoreUInt(uint64(OpOwnerClientWithdraw), 32).
		MustStoreUInt(0, 64).
		MustStoreAddr(sendExcessesTo).
		EndCell(), nil
}

// BuildOwnerRequestRefund builds the start-close op body.
//
//	body = op:0xfafa6cc1 | query_id | excesses_to
func BuildOwnerRequestRefund(sendExcessesTo *address.Address) (*cell.Cell, error) {
	if sendExcessesTo == nil {
		return nil, errors.New("client: nil excessesTo")
	}
	return cell.BeginCell().
		MustStoreUInt(uint64(OpOwnerClientRequestRefund), 32).
		MustStoreUInt(0, 64).
		MustStoreAddr(sendExcessesTo).
		EndCell(), nil
}

// SignedPayload is the inner cell that the proxy signs and the SC verifies.
//
//	signed = op (32) | query_id (64) | new_tokens_used (64) | expected_my_address (Addr)
//
// Used as input to BuildExtChargeSigned and BuildExtGrantRefundSigned.
func SignedPayload(op uint32, queryID uint64, newTokensUsed uint64, expectedMyAddr *address.Address) (*cell.Cell, error) {
	if expectedMyAddr == nil {
		return nil, errors.New("client: nil expectedMyAddr")
	}
	return cell.BeginCell().
		MustStoreUInt(uint64(op), 32).
		MustStoreUInt(queryID, 64).
		MustStoreUInt(newTokensUsed, 64).
		MustStoreAddr(expectedMyAddr).
		EndCell(), nil
}

// BuildExtChargeSigned builds an ext_charge_signed body, given a signed payload.
//
//	body = op:0xbb63ff93 | query_id | excesses_to | signature (64 raw bytes) | ref(signedPayload)
func BuildExtChargeSigned(queryID uint64, signature [64]byte, signedPayload *cell.Cell, sendExcessesTo *address.Address) (*cell.Cell, error) {
	if signedPayload == nil {
		return nil, errors.New("client: nil signedPayload")
	}
	if sendExcessesTo == nil {
		return nil, errors.New("client: nil excessesTo")
	}
	return cell.BeginCell().
		MustStoreUInt(uint64(OpChargeSigned), 32).
		MustStoreUInt(queryID, 64).
		MustStoreAddr(sendExcessesTo).
		MustStoreSlice(signature[:], 64*8).
		MustStoreRef(signedPayload).
		EndCell(), nil
}

// BuildExtGrantRefundSigned builds an ext_grant_refund_signed body.
func BuildExtGrantRefundSigned(queryID uint64, signature [64]byte, signedPayload *cell.Cell, sendExcessesTo *address.Address) (*cell.Cell, error) {
	if signedPayload == nil {
		return nil, errors.New("client: nil signedPayload")
	}
	if sendExcessesTo == nil {
		return nil, errors.New("client: nil excessesTo")
	}
	return cell.BeginCell().
		MustStoreUInt(uint64(OpGrantRefundSigned), 32).
		MustStoreUInt(queryID, 64).
		MustStoreAddr(sendExcessesTo).
		MustStoreSlice(signature[:], 64*8).
		MustStoreRef(signedPayload).
		EndCell(), nil
}

// Storage represents the parsed storage of a cocoon_client SC.
//
// Mirrors the layout from wrappers/CocoonClient.ts: cocoonClientConfigToCell.
type Storage struct {
	State        uint8
	Balance      *big.Int
	Stake        *big.Int
	TokensUsed   uint64
	UnlockTs     uint32
	SecretHash   *big.Int
	OwnerAddress *address.Address
	ProxyAddress *address.Address
	ProxyPubKey  *big.Int
	Params       *cell.Cell
}

// EncodeStorage builds the storage cell for initial deploy.
func EncodeStorage(s Storage) (*cell.Cell, error) {
	if s.OwnerAddress == nil || s.ProxyAddress == nil {
		return nil, errors.New("client: missing addresses")
	}
	if s.Balance == nil {
		s.Balance = big.NewInt(0)
	}
	if s.Stake == nil {
		s.Stake = big.NewInt(0)
	}
	if s.SecretHash == nil {
		s.SecretHash = big.NewInt(0)
	}
	if s.ProxyPubKey == nil {
		return nil, errors.New("client: ProxyPubKey is required")
	}
	if s.Params == nil {
		s.Params = cell.BeginCell().EndCell()
	}
	constData := cell.BeginCell().
		MustStoreAddr(s.OwnerAddress).
		MustStoreAddr(s.ProxyAddress).
		MustStoreBigUInt(s.ProxyPubKey, 256).
		EndCell()
	return cell.BeginCell().
		MustStoreUInt(uint64(s.State), 2).
		MustStoreBigCoins(s.Balance).
		MustStoreBigCoins(s.Stake).
		MustStoreUInt(s.TokensUsed, 64).
		MustStoreUInt(uint64(s.UnlockTs), 32).
		MustStoreBigUInt(s.SecretHash, 256).
		MustStoreRef(constData).
		MustStoreRef(s.Params).
		EndCell(), nil
}

// BuildStateInit builds the client contract StateInit cell from storage and code.
func BuildStateInit(s Storage, code *cell.Cell) (*cell.Cell, error) {
	if code == nil {
		return nil, errors.New("client: code is nil")
	}
	data, err := EncodeStorage(s)
	if err != nil {
		return nil, err
	}
	return cell.BeginCell().
		MustStoreUInt(0, 2).
		MustStoreUInt(3, 2).
		MustStoreRef(code).
		MustStoreRef(data).
		MustStoreUInt(0, 1).
		EndCell(), nil
}

// DeriveAddress computes the client contract address for the given config.
func DeriveAddress(s Storage, code *cell.Cell) (*address.Address, error) {
	init, err := BuildStateInit(s, code)
	if err != nil {
		return nil, err
	}
	hash := init.Hash()
	var raw [32]byte
	copy(raw[:], hash)
	return address.NewAddress(0, 0, raw[:]), nil
}
