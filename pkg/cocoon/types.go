package cocoon

import (
	"crypto/ed25519"
	"errors"
	"log/slog"
	"math/big"
	"time"

	"github.com/TONresistor/gocoon/pkg/contracts/root"
	"github.com/TONresistor/gocoon/pkg/router"
	"github.com/TONresistor/gocoon/pkg/store"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/ton"
)

// PolicyMode controls TLS attestation enforcement.
type PolicyMode int

const (
	// PolicyAny: standard TLS, no TDX verification (v1 default).
	PolicyAny PolicyMode = iota
	// PolicyStrict: RA-TLS strict (v2; returns ErrUnsupported in v1).
	PolicyStrict
)

// Config carries everything required to construct a Client.
type Config struct {
	OwnerAddress     *address.Address
	NodeKey          ed25519.PrivateKey
	RootAddress      *address.Address
	LiteClient       *liteclient.ConnectionPool
	LiteClientConfig string
	Store            store.Store
	Router           router.Dialer
	Logger           *slog.Logger
	PolicyMode       PolicyMode

	// Optional: cocoon-node wallet address (deterministic from NodeKey + wallet code).
	// If nil, the caller must call Client.SetCocoonWalletAddress before any
	// session is opened.
	CocoonWalletAddress *address.Address

	// Optional secret_string used for the short-auth path. Empty = always
	// long-auth (the upstream behavior when secret is unset).
	SecretString string

	// Optional broadcaster used during long-auth to register on-chain and by
	// channel ops. The library does NOT manage owner-wallet keys; the runner
	// or CLI plugs in their own broadcaster.
	Broadcaster Broadcaster
}

// Validate runs basic config sanity checks.
func (c Config) Validate() error {
	if c.OwnerAddress == nil {
		return &ConfigError{Field: "OwnerAddress", Reason: "required"}
	}
	if len(c.NodeKey) != ed25519.PrivateKeySize {
		return &ConfigError{Field: "NodeKey", Reason: "must be 64 bytes (ed25519 private key)"}
	}
	if c.RootAddress == nil {
		return &ConfigError{Field: "RootAddress", Reason: "required"}
	}
	if c.PolicyMode == PolicyStrict {
		return &ConfigError{Field: "PolicyMode", Reason: "PolicyStrict not implemented in v1"}
	}
	return nil
}

// ProxyInfo is one entry from the on-chain registry.
type ProxyInfo struct {
	Seqno       uint32
	NetworkAddr string
}

// SessionStatus enumerates session lifecycle states.
type SessionStatus int

const (
	SessionConnecting SessionStatus = iota
	SessionReady
	SessionDisconnected
	SessionCrashed
)

func (s SessionStatus) String() string {
	switch s {
	case SessionConnecting:
		return "connecting"
	case SessionReady:
		return "ready"
	case SessionDisconnected:
		return "disconnected"
	case SessionCrashed:
		return "crashed"
	}
	return "unknown"
}

// ClientSCSnapshot mirrors the on-chain cocoon_client storage.
type ClientSCSnapshot struct {
	State        uint8
	Balance      *big.Int
	Stake        *big.Int
	TokensUsed   uint64
	UnlockTs     uint32
	SecretHash   *big.Int
	OwnerAddress *address.Address
	ProxyAddress *address.Address
}

// PaymentStatus is the proxy's view of a session's payment state.
type PaymentStatus struct {
	TokensCommittedToBlockchain int64
	TokensCommittedToDB         int64
	TokensMax                   int64
	TokensCharged               int64
	TokensPayed                 int64
}

// WorkerType is one live model exposed by a proxy.
type WorkerType struct {
	Name    string
	Workers []WorkerInstance
}

// WorkerInstance is one active worker for a model.
type WorkerInstance struct {
	Coefficient       int32
	ActiveRequests    int32
	MaxActiveRequests int32
}

// Query is an inference request.
type Query struct {
	Model          string
	Body           []byte
	Path           string
	Headers        map[string]string
	MaxCoefficient int
	MaxTokens      int
	Timeout        time.Duration
	EnableDebug    bool
}

// Chunk is one streaming response part.
type Chunk struct {
	Bytes      []byte
	IsFinal    bool
	RequestID  [32]byte
	TokensUsed TokensUsed
	Err        error
}

// TokensUsed mirrors tl.TokensUsed.
type TokensUsed struct {
	Prompt     int64
	Cached     int64
	Completion int64
	Reasoning  int64
	Total      int64
}

// rootReader returns a reader against the configured root contract.
//
// Builds a tonutils-go API client from cfg.LiteClient if set, otherwise from
// cfg.LiteClientConfig file path.
func (c *Client) rootReader() (*root.Reader, error) {
	pool := c.cfg.LiteClient
	if pool == nil {
		if c.cfg.LiteClientConfig == "" {
			return nil, errors.New("cocoon: no LiteClient or LiteClientConfig configured")
		}
		p := liteclient.NewConnectionPool()
		if err := p.AddConnectionsFromConfigFile(c.cfg.LiteClientConfig); err != nil {
			return nil, err
		}
		pool = p
		c.cfg.LiteClient = pool
	}
	api := ton.NewAPIClient(pool).WithRetryTimeout(3, 0*time.Second)
	return root.NewReader(api, c.cfg.RootAddress.String())
}
