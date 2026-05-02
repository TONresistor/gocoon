package cocoon

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/TONresistor/gocoon/pkg/contracts/wallet"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// Broadcaster sends a TON external message and returns the BoC hash hex.
type Broadcaster interface {
	BroadcastExternal(ctx context.Context, dest *address.Address, body *cell.Cell) (string, error)
}

type initBroadcaster interface {
	BroadcastExternalWithValueAndInit(ctx context.Context, dest *address.Address, body *cell.Cell, valueNano uint64, init *cell.Cell) (string, error)
}

// LiteclientBroadcaster signs a cocoon_wallet external message and broadcasts
// it via a tonutils-go ton.APIClient. This is the production broadcaster used
// by gocoon-runner.
type LiteclientBroadcaster struct {
	API           ton.APIClientWrapped
	NodeKey       ed25519.PrivateKey
	WalletAddress *address.Address
	OwnerAddress  *address.Address
	WalletCode    *cell.Cell // optional; needed only for first-deploy ext-msg
	Logger        *slog.Logger
}

// NewLiteclientBroadcaster builds a Broadcaster from a tonutils ton.API.
func NewLiteclientBroadcaster(api ton.APIClientWrapped, nodeKey ed25519.PrivateKey, walletAddr *address.Address, ownerAddr *address.Address, walletCode *cell.Cell) *LiteclientBroadcaster {
	return &LiteclientBroadcaster{
		API:           api,
		NodeKey:       nodeKey,
		WalletAddress: walletAddr,
		OwnerAddress:  ownerAddr,
		WalletCode:    walletCode,
	}
}

// BroadcastExternal wraps body in a cocoon_wallet outbound transfer and sends
// it on-chain via an external message signed by NodeKey.
//
// The body is sent as an internal message from the cocoon_wallet to dest, with
// 0.5 TON value and bounce=true. Increase value via the OutboundMessage.Value
// field if needed by the caller (we expose a richer overload below).
func (b *LiteclientBroadcaster) BroadcastExternal(ctx context.Context, dest *address.Address, body *cell.Cell) (string, error) {
	return b.broadcastWithValue(ctx, dest, body, defaultMessageValueNano, nil)
}

// BroadcastExternalWithValue is a richer variant that sets the internal
// message value (in nanoTON).
func (b *LiteclientBroadcaster) BroadcastExternalWithValue(ctx context.Context, dest *address.Address, body *cell.Cell, valueNano uint64) (string, error) {
	return b.broadcastWithValue(ctx, dest, body, valueNano, nil)
}

// BroadcastExternalWithValueAndInit sets both the value and an optional init
// cell for the first internal message.
func (b *LiteclientBroadcaster) BroadcastExternalWithValueAndInit(ctx context.Context, dest *address.Address, body *cell.Cell, valueNano uint64, init *cell.Cell) (string, error) {
	return b.broadcastWithValue(ctx, dest, body, valueNano, init)
}

const defaultMessageValueNano = uint64(500_000_000) // 0.5 TON

func (b *LiteclientBroadcaster) broadcastWithValue(ctx context.Context, dest *address.Address, body *cell.Cell, valueNano uint64, init *cell.Cell) (string, error) {
	if b.API == nil {
		return "", errors.New("broadcaster: nil API")
	}
	if len(b.NodeKey) != ed25519.PrivateKeySize {
		return "", errors.New("broadcaster: NodeKey must be ed25519 private key")
	}
	if b.WalletAddress == nil {
		return "", errors.New("broadcaster: WalletAddress nil")
	}
	destBefore, err := b.accountCursor(ctx, dest)
	if err != nil {
		b.logger().Debug("cocoon: destination cursor unavailable", "dest", dest.String(), "err", err)
	}

	// Fetch current seqno to embed in the signed body. If the account is
	// funded but not deployed yet, attach StateInit to the first external send.
	seqno, deployed, err := b.querySeqno(ctx)
	if err != nil {
		return "", fmt.Errorf("broadcaster: get seqno: %w", err)
	}

	mode := uint8(1)
	if valueNano > 1 {
		// Match upstream CocoonWallet::send_pending_transactions: real-value
		// transfers use ordinary mode 0; only dust messages use pay-fees-separately.
		mode = 0
	}
	signed, err := wallet.CreateSignedExternalMessage(
		[]wallet.OutboundMessage{{
			To:      dest,
			Value:   valueNano,
			Body:    body,
			Init:    init,
			Mode:    mode,
			ModeSet: true,
			Bounce:  true,
		}},
		b.NodeKey,
		wallet.SignedExternalMessageOpts{Seqno: seqno},
	)
	if err != nil {
		return "", fmt.Errorf("broadcaster: build ext-msg: %w", err)
	}

	extMsg := &tlb.ExternalMessage{
		DstAddr: b.WalletAddress,
		Body:    signed,
	}
	if !deployed {
		if b.WalletCode == nil {
			return "", errors.New("broadcaster: wallet not deployed and WalletCode nil")
		}
		data, err := wallet.EncodeStorage(wallet.Config{
			PublicKey:    b.NodeKey.Public().(ed25519.PublicKey),
			OwnerAddress: b.OwnerAddress,
		})
		if err != nil {
			return "", fmt.Errorf("broadcaster: build state init data: %w", err)
		}
		extMsg.StateInit = &tlb.StateInit{
			Code: b.WalletCode,
			Data: data,
		}
	}
	tx, block, inMsgHash, err := b.API.SendExternalMessageWaitTransaction(ctx, extMsg)
	if err != nil {
		return hex.EncodeToString(signed.Hash()), fmt.Errorf("broadcaster: send and confirm: %w", err)
	}
	b.logWalletTransaction(tx, dest)
	b.logDestinationTransactions(ctx, block, dest, destBefore)
	return hex.EncodeToString(inMsgHash), nil
}

type accountCursor struct {
	lt   uint64
	hash []byte
}

func (b *LiteclientBroadcaster) logger() *slog.Logger {
	if b.Logger != nil {
		return b.Logger
	}
	return slog.Default()
}

func (b *LiteclientBroadcaster) accountCursor(ctx context.Context, addr *address.Address) (*accountCursor, error) {
	if addr == nil {
		return nil, errors.New("nil address")
	}
	mc, err := b.API.CurrentMasterchainInfo(ctx)
	if err != nil {
		return nil, err
	}
	acc, err := b.API.GetAccount(ctx, mc, addr)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return &accountCursor{}, nil
	}
	return &accountCursor{lt: acc.LastTxLT, hash: acc.LastTxHash}, nil
}

func (b *LiteclientBroadcaster) logWalletTransaction(tx *tlb.Transaction, dest *address.Address) {
	if tx == nil {
		return
	}
	b.logger().Info("cocoon: wallet tx confirmed", append(
		[]any{"lt", tx.LT, "out_count", tx.OutMsgCount},
		transactionPhaseAttrs(tx)...,
	)...)
	if tx.IO.Out == nil {
		return
	}
	out, err := tx.IO.Out.ToSlice()
	if err != nil {
		b.logger().Warn("cocoon: wallet tx out parse failed", "err", err)
		return
	}
	for i, msg := range out {
		if msg.MsgType != tlb.MsgTypeInternal {
			b.logger().Info("cocoon: wallet tx out", "idx", i, "type", msg.MsgType)
			continue
		}
		im := msg.AsInternal()
		matchesDest := dest != nil && im.DstAddr != nil && im.DstAddr.Equals(dest)
		b.logger().Info("cocoon: wallet tx out",
			"idx", i,
			"to", addrString(im.DstAddr),
			"value_nano", im.Amount.Nano().String(),
			"bounce", im.Bounce,
			"bounced", im.Bounced,
			"has_state_init", im.StateInit != nil,
			"body_op", bodyOpHex(im.Body),
			"body_hash", cellHashHex(im.Body),
			"matches_client_sc", matchesDest,
		)
	}
}

func (b *LiteclientBroadcaster) logDestinationTransactions(ctx context.Context, block *ton.BlockIDExt, dest *address.Address, before *accountCursor) {
	if dest == nil || before == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var seqno uint32
	if block != nil {
		seqno = block.SeqNo
	}
	for {
		select {
		case <-ctx.Done():
			b.logger().Warn("cocoon: destination tx not observed", "dest", dest.String(), "last_seen_lt", before.lt)
			return
		default:
		}

		mc, err := b.API.WaitForBlock(seqno).CurrentMasterchainInfo(ctx)
		if err != nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		seqno = mc.SeqNo
		acc, err := b.API.WaitForBlock(mc.SeqNo).GetAccount(ctx, mc, dest)
		if err != nil || acc == nil || acc.LastTxLT == before.lt {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		txs, err := b.API.WaitForBlock(mc.SeqNo).ListTransactions(ctx, dest, 8, acc.LastTxLT, acc.LastTxHash)
		if err != nil {
			b.logger().Warn("cocoon: destination tx list failed", "dest", dest.String(), "err", err)
			return
		}
		for _, tx := range txs {
			if tx == nil || tx.LT <= before.lt {
				continue
			}
			logDestinationTransaction(b.logger(), tx)
		}
		return
	}
}

func logDestinationTransaction(logger *slog.Logger, tx *tlb.Transaction) {
	attrs := []any{"lt", tx.LT, "out_count", tx.OutMsgCount}
	if tx.IO.In != nil {
		attrs = append(attrs,
			"in_type", tx.IO.In.MsgType,
			"in_from", addrString(tx.IO.In.Msg.SenderAddr()),
			"in_op", bodyOpHex(tx.IO.In.Msg.Payload()),
			"in_body_hash", cellHashHex(tx.IO.In.Msg.Payload()),
		)
	}
	attrs = append(attrs, transactionPhaseAttrs(tx)...)
	logger.Info("cocoon: destination tx observed", attrs...)
	if tx.IO.Out == nil {
		return
	}
	out, err := tx.IO.Out.ToSlice()
	if err != nil {
		logger.Warn("cocoon: destination tx out parse failed", "lt", tx.LT, "err", err)
		return
	}
	for i, msg := range out {
		if msg.MsgType != tlb.MsgTypeInternal {
			logger.Info("cocoon: destination tx out", "lt", tx.LT, "idx", i, "type", msg.MsgType)
			continue
		}
		im := msg.AsInternal()
		logger.Info("cocoon: destination tx out",
			"lt", tx.LT,
			"idx", i,
			"to", addrString(im.DstAddr),
			"value_nano", im.Amount.Nano().String(),
			"bounce", im.Bounce,
			"bounced", im.Bounced,
			"has_state_init", im.StateInit != nil,
			"body_op", bodyOpHex(im.Body),
			"body_hash", cellHashHex(im.Body),
		)
	}
}

func transactionPhaseAttrs(tx *tlb.Transaction) []any {
	attrs := []any{}
	desc, ok := tx.Description.(tlb.TransactionDescriptionOrdinary)
	if !ok {
		return attrs
	}
	attrs = append(attrs, "aborted", desc.Aborted, "destroyed", desc.Destroyed)
	switch phase := desc.ComputePhase.Phase.(type) {
	case tlb.ComputePhaseVM:
		attrs = append(attrs,
			"compute_success", phase.Success,
			"compute_exit_code", phase.Details.ExitCode,
			"compute_steps", phase.Details.VMSteps,
		)
	case *tlb.ComputePhaseVM:
		attrs = append(attrs,
			"compute_success", phase.Success,
			"compute_exit_code", phase.Details.ExitCode,
			"compute_steps", phase.Details.VMSteps,
		)
	case tlb.ComputePhaseSkipped:
		attrs = append(attrs, "compute_skipped", phase.Reason.Type)
	case *tlb.ComputePhaseSkipped:
		attrs = append(attrs, "compute_skipped", phase.Reason.Type)
	}
	if desc.ActionPhase != nil {
		attrs = append(attrs,
			"action_success", desc.ActionPhase.Success,
			"action_valid", desc.ActionPhase.Valid,
			"action_no_funds", desc.ActionPhase.NoFunds,
			"action_result_code", desc.ActionPhase.ResultCode,
			"action_total", desc.ActionPhase.TotalActions,
			"action_created", desc.ActionPhase.MessagesCreated,
		)
	}
	if desc.BouncePhase != nil {
		attrs = append(attrs, "has_bounce_phase", true)
	}
	return attrs
}

func bodyOpHex(body *cell.Cell) string {
	if body == nil {
		return ""
	}
	op, err := body.BeginParse().LoadUInt(32)
	if err != nil {
		return "unread"
	}
	return fmt.Sprintf("0x%08x", op)
}

func cellHashHex(c *cell.Cell) string {
	if c == nil {
		return ""
	}
	return hex.EncodeToString(c.Hash())
}

func addrString(addr *address.Address) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

// querySeqno calls the cocoon_wallet seqno get-method. Returns 0 if the
// account is not deployed yet (caller is responsible for handling first
// deploy via init field; the wallet contract layout supports seqno=0 boot).
func (b *LiteclientBroadcaster) querySeqno(ctx context.Context) (uint32, bool, error) {
	mc, err := b.API.CurrentMasterchainInfo(ctx)
	if err != nil {
		return 0, false, err
	}
	res, err := b.API.RunGetMethod(ctx, mc, b.WalletAddress, "seqno")
	if err != nil {
		// Not deployed yet → seqno 0 is a valid starting state.
		return 0, false, nil //nolint:nilerr // intended fallback
	}
	v, err := res.Int(0)
	if err != nil {
		return 0, true, fmt.Errorf("broadcaster: parse seqno: %w", err)
	}
	if !v.IsUint64() {
		return 0, true, errors.New("broadcaster: seqno overflow")
	}
	return uint32(v.Uint64()), true, nil
}

// Compile-time assertion.
var _ Broadcaster = (*LiteclientBroadcaster)(nil)
var _ initBroadcaster = (*LiteclientBroadcaster)(nil)
