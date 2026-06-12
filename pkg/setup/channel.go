package setup

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// ChannelInfo is the on-chain state of a cocoon_client payment channel.
type ChannelInfo struct {
	Address       string
	AccountStatus string
	State         uint8 // 0=active, 1=closing, 2=closed
	StateName     string
	BalanceNano   *big.Int // request balance, spendable on inference
	StakeNano     *big.Int
	TokensUsed    uint64
	UnlockTs      uint32
	OwnerAddress  string
	ProxyAddress  string
}

// Active reports whether the channel can pay for requests right now.
func (c *ChannelInfo) Active() bool {
	return c != nil && c.AccountStatus == "ACTIVE" && c.State == 0
}

// ClientStateName names a cocoon_client state code.
func ClientStateName(state uint8) string {
	switch state {
	case 0:
		return "active"
	case 1:
		return "closing"
	case 2:
		return "closed"
	default:
		return "unknown"
	}
}

var errNotClientSC = errors.New("not a cocoon_client contract")

// FetchChannelInfo reads and parses one cocoon_client contract.
func FetchChannelInfo(ctx context.Context, api ton.APIClientWrapped, addr *address.Address) (*ChannelInfo, error) {
	mc, err := api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("channel: masterchain info: %w", err)
	}
	acc, err := api.GetAccount(ctx, mc, addr)
	if err != nil {
		return nil, fmt.Errorf("channel: get account: %w", err)
	}
	out := &ChannelInfo{
		Address:       addr.String(),
		AccountStatus: "missing",
		StateName:     "missing",
		BalanceNano:   big.NewInt(0),
		StakeNano:     big.NewInt(0),
	}
	if acc == nil || acc.State == nil {
		return out, nil
	}
	out.AccountStatus = fmt.Sprint(acc.State.Status)
	if acc.State.Status != tlb.AccountStatusActive || acc.Data == nil {
		return out, nil
	}
	if err := parseClientStorage(acc.Data, out); err != nil {
		return nil, err
	}
	return out, nil
}

// parseClientStorage decodes the cocoon_client storage layout:
// state(2) balance(coins) stake(coins) tokens_used(64) unlock_ts(32)
// secret_hash(256) ^[owner proxy pubkey(256)] ^params
func parseClientStorage(data *cell.Cell, out *ChannelInfo) error {
	s, err := data.BeginParse()
	if err != nil {
		return fmt.Errorf("channel: begin parse: %w", err)
	}
	state, err := s.LoadUInt(2)
	if err != nil {
		return errNotClientSC
	}
	balance, err := s.LoadBigCoins()
	if err != nil {
		return errNotClientSC
	}
	stake, err := s.LoadBigCoins()
	if err != nil {
		return errNotClientSC
	}
	tokensUsed, err := s.LoadUInt(64)
	if err != nil {
		return errNotClientSC
	}
	unlockTs, err := s.LoadUInt(32)
	if err != nil {
		return errNotClientSC
	}
	if _, err = s.LoadBigUInt(256); err != nil { // secret_hash
		return errNotClientSC
	}
	constData, err := s.LoadRef()
	if err != nil {
		return errNotClientSC
	}
	owner, err := constData.LoadAddr()
	if err != nil {
		return errNotClientSC
	}
	proxy, err := constData.LoadAddr()
	if err != nil {
		return errNotClientSC
	}

	out.State = uint8(state)
	out.StateName = ClientStateName(uint8(state))
	out.BalanceNano = balance
	out.StakeNano = stake
	out.TokensUsed = tokensUsed
	out.UnlockTs = uint32(unlockTs)
	out.OwnerAddress = owner.String()
	out.ProxyAddress = proxy.String()
	return nil
}

// FindChannel scans the node wallet's recent transactions for an existing
// cocoon_client channel funded from it. The client SC address is assigned by
// the proxy during handshake, so before a session exists the transaction
// history is the only local way to rediscover the channel. Read-only.
//
// Returns nil (no error) when no channel is found.
func FindChannel(ctx context.Context, tonConfigPath, nodeAddress string) (*ChannelInfo, error) {
	api, err := NewTONAPI(tonConfigPath)
	if err != nil {
		return nil, err
	}
	node, err := address.ParseAddr(nodeAddress)
	if err != nil {
		return nil, fmt.Errorf("channel: parse node address: %w", err)
	}
	mc, err := api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("channel: masterchain info: %w", err)
	}
	acc, err := api.GetAccount(ctx, mc, node)
	if err != nil {
		return nil, fmt.Errorf("channel: get account: %w", err)
	}
	if acc == nil || !acc.IsActive {
		return nil, nil
	}

	txs, err := api.ListTransactions(ctx, node, 40, acc.LastTxLT, acc.LastTxHash)
	if err != nil {
		return nil, fmt.Errorf("channel: list transactions: %w", err)
	}

	// Candidate destinations of meaningful outgoing transfers, newest first.
	minValue := big.NewInt(900_000_000) // register messages carry 1 TON
	seen := map[string]bool{}
	var candidates []*address.Address
	for i := len(txs) - 1; i >= 0; i-- {
		tx := txs[i]
		if tx.IO.Out == nil {
			continue
		}
		msgs, err := tx.IO.Out.ToSlice()
		if err != nil {
			continue
		}
		for _, m := range msgs {
			if m.MsgType != tlb.MsgTypeInternal {
				continue
			}
			im := m.AsInternal()
			if im.Amount.Nano().Cmp(minValue) < 0 || im.DstAddr == nil {
				continue
			}
			key := im.DstAddr.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, im.DstAddr)
		}
	}

	var best *ChannelInfo
	for _, dst := range candidates {
		info, err := FetchChannelInfo(ctx, api, dst)
		if err != nil {
			continue // not a client SC (or transient read error) — skip
		}
		if info.AccountStatus != "ACTIVE" {
			continue
		}
		// The channel must belong to this wallet.
		if info.OwnerAddress != node.String() {
			continue
		}
		if info.Active() {
			return info, nil
		}
		if best == nil {
			best = info
		}
	}
	return best, nil
}
