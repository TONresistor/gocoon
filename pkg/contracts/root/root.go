// Package root implements read-only access to the cocoon_root contract.
//
// Mainnet root: EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k
// Source FunC: TelegramMessenger/cocoon-contracts/contracts/cocoon_root.fc
//
// Get-method signatures and storage layout sourced from research agent
// (HIGH confidence), cross-validated against cocoon_root.fc and the TS
// wrappers in cocoon-contracts/wrappers/CocoonRoot.ts.
package root

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// MainnetAddress is the cocoon_root contract address on TON mainnet.
const MainnetAddress = "EQCns7bYSp0igFvS1wpb5wsZjCKCV19MD5AVzI4EyxsnU73k"

// Reader is a thin client for read methods on cocoon_root.
type Reader struct {
	api  ton.APIClientWrapped
	addr *address.Address
}

// NewReader builds a Reader for the given root address.
func NewReader(api ton.APIClientWrapped, rootAddr string) (*Reader, error) {
	addr, err := address.ParseAddr(rootAddr)
	if err != nil {
		return nil, fmt.Errorf("root: parse address: %w", err)
	}
	return &Reader{api: api, addr: addr}, nil
}

// Address returns the root contract address.
func (r *Reader) Address() *address.Address { return r.addr }

// Summary mirrors the get_cocoon_data return tuple.
type Summary struct {
	Version           uint32
	LastProxySeqno    uint32
	ParamsVersion     uint32
	UniqueID          uint32
	IsTest            bool
	PricePerToken     *big.Int
	WorkerFeePerToken *big.Int
	MinProxyStake     *big.Int
	MinClientStake    *big.Int
	OwnerAddress      *address.Address
}

// Params mirrors the get_cur_params return tuple (14 fields).
//
// Note: prompt_tokens_price_multiplier and completion_tokens_price_multiplier
// are NOT returned by get_cur_params per the research agent's findings; they
// require raw account-state parsing (covered separately in ReadFullParams).
type Params struct {
	ParamsVersion            uint32
	UniqueID                 uint32
	IsTest                   bool
	PricePerToken            *big.Int
	WorkerFeePerToken        *big.Int
	CachedTokensPriceMult    uint32
	ReasoningTokensPriceMult uint32
	ProxyDelayBeforeClose    uint32
	ClientDelayBeforeClose   uint32
	MinProxyStake            *big.Int
	MinClientStake           *big.Int
	ProxySCHash              *big.Int // uint256 (0 if absent)
	WorkerSCHash             *big.Int
	ClientSCHash             *big.Int
}

// StateSnapshot exposes the full root account params cell, including the
// contract code refs needed to derive worker/client addresses or to rebuild
// client params without code refs.
type StateSnapshot struct {
	StructVersion uint8
	ParamsVersion uint32
	UniqueID      uint32
	IsTest        bool

	PricePerToken             *big.Int
	WorkerFeePerToken         *big.Int
	PromptTokensPriceMult     uint32
	CachedTokensPriceMult     uint32
	CompletionTokensPriceMult uint32
	ReasoningTokensPriceMult  uint32
	ProxyDelayBeforeClose     uint32
	ClientDelayBeforeClose    uint32
	MinProxyStake             *big.Int
	MinClientStake            *big.Int

	ProxySCCode  *cell.Cell
	WorkerSCCode *cell.Cell
	ClientSCCode *cell.Cell
}

// ClientParamsCell rebuilds the params cell used by cocoon_client init, with
// all contract-code refs stripped out.
func (s StateSnapshot) ClientParamsCell() (*cell.Cell, error) {
	return encodeParamsCell(s, false)
}

// ClientStateInitData builds the cocoon_client storage cell for deployment.
func (s StateSnapshot) ClientStateInitData(owner, proxy *address.Address, proxyPubKey [32]byte) (*cell.Cell, error) {
	return clientInitData(owner, proxy, proxyPubKey, s.MinClientStake, s.ClientParamsCell)
}

// GetSummary calls get_cocoon_data on a recent masterchain block.
func (r *Reader) GetSummary(ctx context.Context) (*Summary, error) {
	mc, err := r.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("root: masterchain info: %w", err)
	}
	res, err := r.api.RunGetMethod(ctx, mc, r.addr, "get_cocoon_data")
	if err != nil {
		return nil, fmt.Errorf("root: get_cocoon_data: %w", err)
	}
	if n := len(res.AsTuple()); n < 10 {
		return nil, fmt.Errorf("root: get_cocoon_data returned %d items, want >= 10", n)
	}
	getInt := func(i uint) (*big.Int, error) {
		v, err := res.Int(i)
		if err != nil {
			return nil, fmt.Errorf("root: stack[%d]: %w", i, err)
		}
		return v, nil
	}
	getU32 := func(i uint) (uint32, error) {
		v, err := getInt(i)
		if err != nil {
			return 0, err
		}
		return uint32(v.Uint64()), nil
	}

	s := &Summary{}
	if s.Version, err = getU32(0); err != nil {
		return nil, err
	}
	if s.LastProxySeqno, err = getU32(1); err != nil {
		return nil, err
	}
	if s.ParamsVersion, err = getU32(2); err != nil {
		return nil, err
	}
	if s.UniqueID, err = getU32(3); err != nil {
		return nil, err
	}
	isTest, err := getInt(4)
	if err != nil {
		return nil, err
	}
	s.IsTest = isTest.Sign() != 0
	if s.PricePerToken, err = getInt(5); err != nil {
		return nil, err
	}
	if s.WorkerFeePerToken, err = getInt(6); err != nil {
		return nil, err
	}
	if s.MinProxyStake, err = getInt(7); err != nil {
		return nil, err
	}
	if s.MinClientStake, err = getInt(8); err != nil {
		return nil, err
	}
	ownerSlice, err := res.Slice(9)
	if err != nil {
		return nil, fmt.Errorf("root: stack[9] owner: %w", err)
	}
	addr, err := ownerSlice.LoadAddr()
	if err != nil {
		return nil, fmt.Errorf("root: parse owner addr: %w", err)
	}
	s.OwnerAddress = addr
	return s, nil
}

// GetParams calls get_cur_params on a recent masterchain block.
func (r *Reader) GetParams(ctx context.Context) (*Params, error) {
	mc, err := r.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("root: masterchain info: %w", err)
	}
	res, err := r.api.RunGetMethod(ctx, mc, r.addr, "get_cur_params")
	if err != nil {
		return nil, fmt.Errorf("root: get_cur_params: %w", err)
	}
	if n := len(res.AsTuple()); n < 14 {
		return nil, fmt.Errorf("root: get_cur_params returned %d items, want >= 14", n)
	}

	getInt := func(i uint) (*big.Int, error) {
		v, err := res.Int(i)
		if err != nil {
			return nil, fmt.Errorf("root: stack[%d]: %w", i, err)
		}
		return v, nil
	}
	getU32 := func(i uint) (uint32, error) {
		v, err := getInt(i)
		if err != nil {
			return 0, err
		}
		return uint32(v.Uint64()), nil
	}

	p := &Params{}
	if p.ParamsVersion, err = getU32(0); err != nil {
		return nil, err
	}
	if p.UniqueID, err = getU32(1); err != nil {
		return nil, err
	}
	isTest, err := getInt(2)
	if err != nil {
		return nil, err
	}
	p.IsTest = isTest.Sign() != 0
	if p.PricePerToken, err = getInt(3); err != nil {
		return nil, err
	}
	if p.WorkerFeePerToken, err = getInt(4); err != nil {
		return nil, err
	}
	if p.CachedTokensPriceMult, err = getU32(5); err != nil {
		return nil, err
	}
	if p.ReasoningTokensPriceMult, err = getU32(6); err != nil {
		return nil, err
	}
	if p.ProxyDelayBeforeClose, err = getU32(7); err != nil {
		return nil, err
	}
	if p.ClientDelayBeforeClose, err = getU32(8); err != nil {
		return nil, err
	}
	if p.MinProxyStake, err = getInt(9); err != nil {
		return nil, err
	}
	if p.MinClientStake, err = getInt(10); err != nil {
		return nil, err
	}
	if p.ProxySCHash, err = getInt(11); err != nil {
		return nil, err
	}
	if p.WorkerSCHash, err = getInt(12); err != nil {
		return nil, err
	}
	if p.ClientSCHash, err = getInt(13); err != nil {
		return nil, err
	}
	return p, nil
}

// LoadStateSnapshot parses the root account state and returns the current
// params cell plus the deployed contract codes. This mirrors the upstream
// RootContractConfig::load_from_state layout.
func (r *Reader) LoadStateSnapshot(ctx context.Context) (*StateSnapshot, error) {
	mc, err := r.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("root: masterchain info: %w", err)
	}
	acc, err := r.api.GetAccount(ctx, mc, r.addr)
	if err != nil {
		return nil, fmt.Errorf("root: get account: %w", err)
	}
	if acc.Data == nil {
		return nil, errors.New("root: account data is nil")
	}
	return parseStateSnapshot(acc.Data)
}

// LastProxySeqno calls last_proxy_seqno.
func (r *Reader) LastProxySeqno(ctx context.Context) (uint32, error) {
	mc, err := r.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return 0, fmt.Errorf("root: masterchain info: %w", err)
	}
	res, err := r.api.RunGetMethod(ctx, mc, r.addr, "last_proxy_seqno")
	if err != nil {
		return 0, fmt.Errorf("root: last_proxy_seqno: %w", err)
	}
	v, err := res.Int(0)
	if err != nil {
		return 0, err
	}
	return uint32(v.Uint64()), nil
}

func parseStateSnapshot(top *cell.Cell) (*StateSnapshot, error) {
	s, err := top.BeginParse()
	if err != nil {
		return nil, fmt.Errorf("root: top: begin parse: %w", err)
	}
	if _, err := s.LoadAddr(); err != nil {
		return nil, fmt.Errorf("root: top: load owner addr: %w", err)
	}
	if _, err := s.LoadUInt(32); err != nil {
		return nil, fmt.Errorf("root: top: load version: %w", err)
	}
	if _, err := s.LoadRef(); err != nil {
		return nil, fmt.Errorf("root: top: load data ref: %w", err)
	}
	paramsRef, err := s.LoadRef()
	if err != nil {
		return nil, fmt.Errorf("root: top: load params ref: %w", err)
	}
	paramsCell, err := paramsRef.ToCell()
	if err != nil {
		return nil, fmt.Errorf("root: top: params cell: %w", err)
	}
	snap, err := parseParamsCell(paramsCell)
	if err != nil {
		return nil, err
	}
	if snap.StructVersion >= 4 {
		if _, err := s.LoadRef(); err != nil {
			return nil, fmt.Errorf("root: top: load key manager ref: %w", err)
		}
	}
	return snap, nil
}

func parseParamsCell(params *cell.Cell) (*StateSnapshot, error) {
	if params == nil {
		return nil, errors.New("root: params cell is nil")
	}
	s, err := params.BeginParse()
	if err != nil {
		return nil, fmt.Errorf("root: params: begin parse: %w", err)
	}
	snap := &StateSnapshot{}

	if snap.StructVersion, err = readU8(s, "struct_version"); err != nil {
		return nil, err
	}
	if snap.StructVersion > 5 {
		return nil, fmt.Errorf("root: unexpected params struct version: %d", snap.StructVersion)
	}
	if snap.ParamsVersion, err = readU32(s, "params_version"); err != nil {
		return nil, err
	}
	if snap.UniqueID, err = readU32(s, "unique_id"); err != nil {
		return nil, err
	}
	if snap.IsTest, err = readBool(s, "is_test"); err != nil {
		return nil, err
	}
	if snap.PricePerToken, err = readCoins(s, "price_per_token"); err != nil {
		return nil, err
	}
	if snap.WorkerFeePerToken, err = readCoins(s, "worker_fee_per_token"); err != nil {
		return nil, err
	}
	if snap.StructVersion >= 3 {
		if snap.PromptTokensPriceMult, err = readU32(s, "prompt_tokens_price_multiplier"); err != nil {
			return nil, err
		}
	}
	if snap.StructVersion >= 2 {
		if snap.CachedTokensPriceMult, err = readU32(s, "cached_tokens_price_multiplier"); err != nil {
			return nil, err
		}
	}
	if snap.StructVersion >= 3 {
		if snap.CompletionTokensPriceMult, err = readU32(s, "completion_tokens_price_multiplier"); err != nil {
			return nil, err
		}
	}
	if snap.StructVersion >= 2 {
		if snap.ReasoningTokensPriceMult, err = readU32(s, "reasoning_tokens_price_multiplier"); err != nil {
			return nil, err
		}
	}
	if snap.ProxyDelayBeforeClose, err = readU32(s, "proxy_delay_before_close"); err != nil {
		return nil, err
	}
	if snap.ClientDelayBeforeClose, err = readU32(s, "client_delay_before_close"); err != nil {
		return nil, err
	}
	if snap.StructVersion >= 1 {
		if snap.MinProxyStake, err = readCoins(s, "min_proxy_stake"); err != nil {
			return nil, err
		}
		if snap.MinClientStake, err = readCoins(s, "min_client_stake"); err != nil {
			return nil, err
		}
	}
	if snap.ProxySCCode, err = readMaybeRef(s, "proxy_sc_code"); err != nil {
		return nil, err
	}
	if snap.WorkerSCCode, err = readMaybeRef(s, "worker_sc_code"); err != nil {
		return nil, err
	}
	if snap.ClientSCCode, err = readMaybeRef(s, "client_sc_code"); err != nil {
		return nil, err
	}
	return snap, nil
}

func encodeParamsCell(s StateSnapshot, includeCodes bool) (*cell.Cell, error) {
	b := cell.BeginCell().
		MustStoreUInt(uint64(s.StructVersion), 8).
		MustStoreUInt(uint64(s.ParamsVersion), 32).
		MustStoreUInt(uint64(s.UniqueID), 32).
		MustStoreBoolBit(s.IsTest).
		MustStoreBigCoins(s.PricePerToken).
		MustStoreBigCoins(s.WorkerFeePerToken)
	if s.StructVersion >= 3 {
		b = b.MustStoreUInt(uint64(s.PromptTokensPriceMult), 32)
	}
	if s.StructVersion >= 2 {
		b = b.MustStoreUInt(uint64(s.CachedTokensPriceMult), 32)
	}
	if s.StructVersion >= 3 {
		b = b.MustStoreUInt(uint64(s.CompletionTokensPriceMult), 32)
	}
	if s.StructVersion >= 2 {
		b = b.MustStoreUInt(uint64(s.ReasoningTokensPriceMult), 32)
	}
	b = b.MustStoreUInt(uint64(s.ProxyDelayBeforeClose), 32).
		MustStoreUInt(uint64(s.ClientDelayBeforeClose), 32)
	if s.StructVersion >= 1 {
		b = b.MustStoreBigCoins(s.MinProxyStake).MustStoreBigCoins(s.MinClientStake)
	}
	if includeCodes {
		b = b.MustStoreMaybeRef(s.ProxySCCode).MustStoreMaybeRef(s.WorkerSCCode).MustStoreMaybeRef(s.ClientSCCode)
	} else {
		b = b.MustStoreMaybeRef(nil).MustStoreMaybeRef(nil).MustStoreMaybeRef(nil)
	}
	return b.EndCell(), nil
}

func clientInitData(owner, proxy *address.Address, proxyPubKey [32]byte, minClientStake *big.Int, paramsFn func() (*cell.Cell, error)) (*cell.Cell, error) {
	if owner == nil || proxy == nil {
		return nil, errors.New("root: missing init addresses")
	}
	if minClientStake == nil {
		return nil, errors.New("root: missing min client stake")
	}
	params, err := paramsFn()
	if err != nil {
		return nil, err
	}
	constData := cell.BeginCell().
		MustStoreAddr(owner).
		MustStoreAddr(proxy).
		MustStoreSlice(proxyPubKey[:], 256).
		EndCell()
	return cell.BeginCell().
		MustStoreUInt(0, 2).
		MustStoreBigCoins(big.NewInt(0)).
		MustStoreBigCoins(minClientStake).
		MustStoreUInt(0, 64).
		MustStoreUInt(0, 32).
		MustStoreUInt(0, 256).
		MustStoreRef(constData).
		MustStoreRef(params).
		EndCell(), nil
}

func readU8(s *cell.Slice, field string) (uint8, error) {
	v, err := s.LoadUInt(8)
	if err != nil {
		return 0, fmt.Errorf("root: params: %s: %w", field, err)
	}
	return uint8(v), nil
}

func readU32(s *cell.Slice, field string) (uint32, error) {
	v, err := s.LoadUInt(32)
	if err != nil {
		return 0, fmt.Errorf("root: params: %s: %w", field, err)
	}
	return uint32(v), nil
}

func readBool(s *cell.Slice, field string) (bool, error) {
	v, err := s.LoadBoolBit()
	if err != nil {
		return false, fmt.Errorf("root: params: %s: %w", field, err)
	}
	return v, nil
}

func readCoins(s *cell.Slice, field string) (*big.Int, error) {
	v, err := s.LoadBigCoins()
	if err != nil {
		return nil, fmt.Errorf("root: params: %s: %w", field, err)
	}
	return v, nil
}

func readMaybeRef(s *cell.Slice, field string) (*cell.Cell, error) {
	ref, err := s.LoadMaybeRef()
	if err != nil {
		return nil, fmt.Errorf("root: params: %s: %w", field, err)
	}
	if ref == nil {
		return nil, nil
	}
	return ref.ToCell()
}

// IsProxyHashValid calls proxy_hash_is_valid(hash).
func (r *Reader) IsProxyHashValid(ctx context.Context, hash *big.Int) (bool, error) {
	return r.checkHash(ctx, "proxy_hash_is_valid", hash)
}

// IsWorkerHashValid calls worker_hash_is_valid(hash).
func (r *Reader) IsWorkerHashValid(ctx context.Context, hash *big.Int) (bool, error) {
	return r.checkHash(ctx, "worker_hash_is_valid", hash)
}

// IsModelHashValid calls model_hash_is_valid(hash).
func (r *Reader) IsModelHashValid(ctx context.Context, hash *big.Int) (bool, error) {
	return r.checkHash(ctx, "model_hash_is_valid", hash)
}

// IsPublicKeyValid calls public_key_is_valid(hash).
func (r *Reader) IsPublicKeyValid(ctx context.Context, hash *big.Int) (bool, error) {
	return r.checkHash(ctx, "public_key_is_valid", hash)
}

func (r *Reader) checkHash(ctx context.Context, method string, hash *big.Int) (bool, error) {
	if hash == nil {
		return false, errors.New("root: nil hash")
	}
	mc, err := r.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return false, err
	}
	res, err := r.api.RunGetMethod(ctx, mc, r.addr, method, hash)
	if err != nil {
		return false, fmt.Errorf("root: %s: %w", method, err)
	}
	v, err := res.Int(0)
	if err != nil {
		return false, err
	}
	// FunC returns -1 (true) or 0 (false).
	return v.Sign() != 0, nil
}

// ProxyEntry is one entry from registered_proxies dict.
type ProxyEntry struct {
	Seqno       uint32
	NetworkAddr string // e.g. "1.2.3.4:8080"
}

// RegisteredProxies enumerates the registered_proxies dict by reading the
// raw account state and walking the HashmapE(32, net_addr_slice) structure.
//
// Per research agent: the dict has NO get-method, so we go through raw state.
func (r *Reader) RegisteredProxies(ctx context.Context) ([]ProxyEntry, error) {
	mc, err := r.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("root: masterchain info: %w", err)
	}
	acc, err := r.api.GetAccount(ctx, mc, r.addr)
	if err != nil {
		return nil, fmt.Errorf("root: get account: %w", err)
	}
	if acc.Data == nil {
		return nil, errors.New("root: account data is nil")
	}
	dict, err := loadRegisteredProxiesDict(acc.Data)
	if err != nil {
		return nil, err
	}
	if dict == nil {
		return nil, nil
	}
	entries, err := dict.LoadAll()
	if err != nil {
		return nil, fmt.Errorf("root: dict.LoadAll: %w", err)
	}
	out := make([]ProxyEntry, 0, len(entries))
	for _, kv := range entries {
		seqnoBig, err := kv.Key.LoadBigUInt(32)
		if err != nil {
			return nil, fmt.Errorf("root: key: %w", err)
		}
		valSlice := kv.Value
		// net_addr layout: [bit:0 type_flag][uint7: strlen][8*strlen bits string]
		typeFlag, err := valSlice.LoadBoolBit()
		if err != nil {
			return nil, fmt.Errorf("root: net_addr type_flag: %w", err)
		}
		if typeFlag {
			return nil, errors.New("root: net_addr type_flag != 0")
		}
		strlen, err := valSlice.LoadUInt(7)
		if err != nil {
			return nil, fmt.Errorf("root: net_addr strlen: %w", err)
		}
		buf, err := valSlice.LoadSlice(uint(strlen) * 8)
		if err != nil {
			return nil, fmt.Errorf("root: net_addr bytes: %w", err)
		}
		out = append(out, ProxyEntry{
			Seqno:       uint32(seqnoBig.Uint64()),
			NetworkAddr: string(buf),
		})
	}
	return out, nil
}

// loadRegisteredProxiesDict walks the top-level cell to find the
// registered_proxies HashmapE.
//
// Layout (per research agent):
//
//	top-level cell:
//	  bits  : MsgAddress (267 bits) owner_address; uint32 version
//	  ref[0]: data_cell
//	    bits: HashmapE(256, empty) proxy_hashes; HashmapE(32, net_addr) registered_proxies;
//	          uint32 last_proxy_seqno; HashmapE(256, empty) worker_hashes; HashmapE(256, empty) model_hashes
//	  ref[1]: params_cell
//	  ref[2]: public_keys_manager_cell
func loadRegisteredProxiesDict(top *cell.Cell) (*cell.Dictionary, error) {
	s, err := top.BeginParse()
	if err != nil {
		return nil, fmt.Errorf("root: top: begin parse: %w", err)
	}
	if _, err := s.LoadAddr(); err != nil {
		return nil, fmt.Errorf("root: top: load owner addr: %w", err)
	}
	if _, err := s.LoadUInt(32); err != nil {
		return nil, fmt.Errorf("root: top: load version: %w", err)
	}
	dataRef, err := s.LoadRef()
	if err != nil {
		return nil, fmt.Errorf("root: top: load data ref: %w", err)
	}
	// data_cell:
	if _, err := dataRef.LoadDict(256); err != nil {
		return nil, fmt.Errorf("root: data: load proxy_hashes: %w", err)
	}
	d, err := dataRef.LoadDict(32)
	if err != nil {
		return nil, fmt.Errorf("root: data: load registered_proxies: %w", err)
	}
	return d, nil
}
