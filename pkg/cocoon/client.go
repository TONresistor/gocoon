package cocoon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"sync/atomic"

	clientcontract "github.com/TONresistor/gocoon/pkg/contracts/client"
	"github.com/TONresistor/gocoon/pkg/contracts/root"
	"github.com/TONresistor/gocoon/pkg/router"
	memstore "github.com/TONresistor/gocoon/pkg/store/memory"
	"github.com/TONresistor/gocoon/pkg/tl"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// Client is the top-level cocoon client.
type Client struct {
	cfg    Config
	logger *slog.Logger
	router router.Dialer

	mu       sync.RWMutex
	sessions map[string]*Session
	closed   bool

	// rootCfgVer is the last observed root contract `params_version`. The
	// session handshake sends it as min_config_version. Updated by the root
	// poller (when wired).
	rootCfgVer atomic.Int32

	// cocoonWalletAddr is the bounceable string address of the cocoon-node
	// wallet, derived from the node key + wallet code at startup.
	cocoonWalletAddr atomic.Pointer[address.Address]
}

// New constructs a Client.
func New(_ context.Context, cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Store == nil {
		logger.Warn("cocoon: no store provided, using in-memory")
		cfg.Store = memstore.New()
	}
	r := cfg.Router
	if r == nil {
		r = &router.DefaultDialer{}
	}
	c := &Client{
		cfg:      cfg,
		logger:   logger,
		router:   r,
		sessions: make(map[string]*Session),
	}
	// If caller supplied a precomputed cocoon wallet address, store it.
	if cfg.CocoonWalletAddress != nil {
		c.cocoonWalletAddr.Store(cfg.CocoonWalletAddress)
	}
	return c, nil
}

// Logger returns the configured logger.
func (c *Client) Logger() *slog.Logger { return c.logger }

// RootAddressString returns the configured root contract address as a string.
func (c *Client) RootAddressString() string {
	if c.cfg.RootAddress == nil {
		return ""
	}
	return c.cfg.RootAddress.String()
}

// Close releases all sessions and resources.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrAlreadyClosed
	}
	c.closed = true
	sessions := c.sessions
	c.sessions = nil
	c.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}
	if c.cfg.Store != nil {
		_ = c.cfg.Store.Close()
	}
	return nil
}

// Sessions returns a snapshot of currently registered sessions.
func (c *Client) Sessions() []*Session {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Session, 0, len(c.sessions))
	for _, s := range c.sessions {
		out = append(out, s)
	}
	return out
}

// SetCocoonWalletAddress is the deferred way to inject the cocoon-node wallet
// address (often derived from key+code lazily).
func (c *Client) SetCocoonWalletAddress(addr *address.Address) {
	c.cocoonWalletAddr.Store(addr)
}

// SetRootConfigVersion is called by the root poller to update the version we
// claim in handshakes.
func (c *Client) SetRootConfigVersion(v int32) {
	c.rootCfgVer.Store(v)
}

func (c *Client) cocoonWalletAddrStr() string {
	if a := c.cocoonWalletAddr.Load(); a != nil {
		return a.String()
	}
	return ""
}

func (c *Client) rootConfigVersion() int32 { return c.rootCfgVer.Load() }

// registerOnChain is the closure passed to the handshake to perform the
// long-auth on-chain register message. Upstream deploys cocoon_client on the
// first send, then broadcasts owner_client_register to that contract.
func (c *Client) registerOnChain(ctx context.Context, clientSCAddrStr string, nonce uint64, proxy tl.ProxyParams) error {
	if c.cfg.Broadcaster == nil {
		return errors.New("cocoon: long-auth requires Broadcaster but none configured")
	}
	clientSC, err := address.ParseAddr(clientSCAddrStr)
	if err != nil {
		return err
	}
	excessesTo := c.cocoonWalletAddr.Load()
	if excessesTo == nil {
		return errors.New("cocoon: cocoon wallet address not set")
	}
	initCell := (*cell.Cell)(nil)
	topUpAmount := (*big.Int)(nil)
	if br, ok := c.cfg.Broadcaster.(*LiteclientBroadcaster); ok && br.API != nil {
		rootReader, err := root.NewReader(br.API, c.cfg.RootAddress.String())
		if err != nil {
			return err
		}
		snapshot, err := rootReader.LoadStateSnapshot(ctx)
		if err != nil {
			return err
		}
		if snapshot.ClientSCCode == nil {
			return errors.New("cocoon: root client_sc_code is nil")
		}
		if snapshot.MinClientStake == nil || snapshot.MinClientStake.Sign() <= 0 {
			return errors.New("cocoon: root min_client_stake is invalid")
		}
		paramsCell, err := snapshot.ClientParamsCell()
		if err != nil {
			return err
		}
		proxyAddr, err := address.ParseAddr(proxy.SCAddress)
		if err != nil {
			return fmt.Errorf("cocoon: parse proxy_sc_address: %w", err)
		}
		storage := clientcontract.Storage{
			State:        0,
			Balance:      big.NewInt(0),
			Stake:        snapshot.MinClientStake,
			TokensUsed:   0,
			UnlockTs:     0,
			SecretHash:   big.NewInt(0),
			OwnerAddress: excessesTo,
			ProxyAddress: proxyAddr,
			ProxyPubKey:  new(big.Int).SetBytes(proxy.PublicKey[:]),
			Params:       paramsCell,
		}
		derivedAddr, err := clientcontract.DeriveAddress(storage, snapshot.ClientSCCode)
		if err != nil {
			return err
		}
		if !derivedAddr.Equals(clientSC) {
			return fmt.Errorf("cocoon: client init address mismatch: derived=%s proxy=%s raw_derived=%s raw_proxy=%s",
				derivedAddr.String(), clientSC.String(), derivedAddr.StringRaw(), clientSC.StringRaw())
		}

		mc, err := br.API.CurrentMasterchainInfo(ctx)
		if err != nil {
			return fmt.Errorf("cocoon: client account check: %w", err)
		}
		acc, err := br.API.GetAccount(ctx, mc, clientSC)
		if err != nil {
			return fmt.Errorf("cocoon: client account fetch: %w", err)
		}
		var status tlb.AccountStatus = tlb.AccountStatusNonExist
		if acc != nil && acc.State != nil {
			status = acc.State.Status
		}
		active := status == tlb.AccountStatusActive && acc.Code != nil
		currentBalance := big.NewInt(0)
		if active && acc.Data != nil {
			if bal, err := decodeClientRequestBalance(acc.Data); err != nil {
				c.logger.Warn("cocoon: client balance decode failed", "client_sc", clientSCAddrStr, "err", err)
			} else {
				currentBalance = bal
			}
		}
		if currentBalance.Cmp(snapshot.MinClientStake) < 0 {
			topUpAmount = new(big.Int).Sub(snapshot.MinClientStake, currentBalance)
		}
		c.logger.Info("cocoon: client deployment state",
			"client_sc", clientSCAddrStr,
			"status", status,
			"active", active,
			"request_balance_nano", currentBalance.String(),
			"min_client_stake_nano", snapshot.MinClientStake.String(),
		)
		if !active {
			initCell, err = clientcontract.BuildStateInit(storage, snapshot.ClientSCCode)
			if err != nil {
				return fmt.Errorf("cocoon: client init: %w", err)
			}
			c.logger.Info("cocoon: client init prepared", "client_sc", clientSCAddrStr, "proxy_sc", proxy.SCAddress, "nonce", nonce)
		}
	}

	body, err := buildProxyRegisterMessage(nonce, excessesTo)
	if err != nil {
		return err
	}
	const registerValueNano = uint64(1_000_000_000)
	bocHash, err := broadcastExternalWithValueAndInit(ctx, c.cfg.Broadcaster, clientSC, body, registerValueNano, initCell)
	if err != nil {
		return &ChainError{Op: "broadcast", BocHash: bocHash, Inner: err}
	}
	c.logger.Info("cocoon: register message broadcast", "boc", bocHash, "client_sc", clientSCAddrStr, "value_nano", registerValueNano)
	if topUpAmount != nil && topUpAmount.Sign() > 0 {
		if !topUpAmount.IsUint64() {
			return errors.New("cocoon: top-up amount overflows uint64")
		}
		topUpBody, err := clientcontract.BuildExtTopUp(topUpAmount, excessesTo)
		if err != nil {
			return err
		}
		topUpValue := topUpAmount.Uint64() + 700_000_000
		topUpHash, err := broadcastExternalWithValue(ctx, c.cfg.Broadcaster, clientSC, topUpBody, topUpValue)
		if err != nil {
			return &ChainError{Op: "topup", BocHash: topUpHash, Inner: err}
		}
		c.logger.Info("cocoon: top-up message broadcast",
			"boc", topUpHash,
			"client_sc", clientSCAddrStr,
			"top_up_nano", topUpAmount.String(),
			"value_nano", topUpValue,
		)
	}
	return nil
}

func decodeClientRequestBalance(data *cell.Cell) (*big.Int, error) {
	if data == nil {
		return big.NewInt(0), nil
	}
	s := data.BeginParse()
	if _, err := s.LoadUInt(2); err != nil {
		return nil, err
	}
	return s.LoadBigCoins()
}

type valueBroadcaster interface {
	BroadcastExternalWithValue(ctx context.Context, dest *address.Address, body *cell.Cell, valueNano uint64) (string, error)
}

func broadcastExternalWithValue(ctx context.Context, b Broadcaster, dest *address.Address, body *cell.Cell, valueNano uint64) (string, error) {
	if vb, ok := b.(valueBroadcaster); ok {
		return vb.BroadcastExternalWithValue(ctx, dest, body, valueNano)
	}
	return b.BroadcastExternal(ctx, dest, body)
}

type valueInitBroadcaster interface {
	BroadcastExternalWithValueAndInit(ctx context.Context, dest *address.Address, body *cell.Cell, valueNano uint64, init *cell.Cell) (string, error)
}

func broadcastExternalWithValueAndInit(ctx context.Context, b Broadcaster, dest *address.Address, body *cell.Cell, valueNano uint64, init *cell.Cell) (string, error) {
	if vb, ok := b.(valueInitBroadcaster); ok {
		return vb.BroadcastExternalWithValueAndInit(ctx, dest, body, valueNano, init)
	}
	if init != nil {
		return "", errors.New("cocoon: broadcaster does not support init messages")
	}
	return broadcastExternalWithValue(ctx, b, dest, body, valueNano)
}

// newChannelOps builds a ChannelOps for a session's deployed cocoon_client SC.
func (c *Client) newChannelOps(clientSCAddrStr string) (*ChannelOps, error) {
	clientSC, err := address.ParseAddr(clientSCAddrStr)
	if err != nil {
		return nil, err
	}
	excessesTo := c.cocoonWalletAddr.Load()
	if excessesTo == nil {
		return nil, errors.New("cocoon: cocoon wallet address not set")
	}
	return NewChannelOps(clientSC, excessesTo)
}

// RegisteredProxies discovers proxies via the on-chain root contract.
//
// Requires cfg.LiteClient or cfg.LiteClientConfig to be set so we can build
// a TON API client. Returns ErrUnsupported if neither is configured.
func (c *Client) RegisteredProxies(ctx context.Context) ([]ProxyInfo, error) {
	rdr, err := c.rootReader()
	if err != nil {
		return nil, err
	}
	entries, err := rdr.RegisteredProxies(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ProxyInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, ProxyInfo{
			Seqno:       e.Seqno,
			NetworkAddr: e.NetworkAddr,
		})
	}
	return out, nil
}

// ConnectProxy opens (or returns the existing) Session to the proxy at addr.
//
// `addr` is a network "host:port". For discovery via the root contract use
// RegisteredProxies first.
func (c *Client) ConnectProxy(ctx context.Context, addr string) (*Session, error) {
	if addr == "" {
		return nil, errors.New("cocoon: empty proxy address")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrAlreadyClosed
	}
	if existing, ok := c.sessions[addr]; ok {
		if existing.isUsable() {
			c.mu.Unlock()
			return existing, nil
		}
		delete(c.sessions, addr)
		c.mu.Unlock()
		_ = existing.Close()
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, ErrAlreadyClosed
		}
	}
	s := newSession(c, addr)
	c.sessions[addr] = s
	c.mu.Unlock()

	if err := s.connect(ctx); err != nil {
		c.removeSession(addr)
		return nil, err
	}
	return s, nil
}

// DisconnectProxy closes and removes the session for addr.
func (c *Client) DisconnectProxy(_ context.Context, addr string) error {
	c.removeSession(addr)
	return nil
}

func (c *Client) removeSession(addr string) {
	c.mu.Lock()
	s, ok := c.sessions[addr]
	if ok {
		delete(c.sessions, addr)
	}
	c.mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}
