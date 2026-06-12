package core

import "sync"

// RunnerState is the shared mutable status snapshot: updated by the engine's
// discovery loop, read by HTTP handlers (/jsonstats, /api/state, gateway
// health). Field names mirror the JSON the browser parses.
type RunnerState struct {
	mu               sync.RWMutex
	WalletBalance    uint64
	TONLastSyncedAt  int64
	Enabled          bool
	GitCommit        string
	RootAddress      string
	OwnerAddress     string
	CheckImageHashes bool
	ProxyConnections []ProxyConnectionEntry
	Proxies          []ProxyStatEntry
}

// ProxyConnectionEntry mirrors the JSON shape the browser parses.
type ProxyConnectionEntry struct {
	Address        string `json:"address"`
	IsReady        bool   `json:"is_ready"`
	ProxySCAddress string `json:"proxy_sc_address"`
}

// ProxyStatEntry mirrors RunnerProxyStat in the browser TS interface.
type ProxyStatEntry struct {
	ProxySCAddress                       string `json:"proxy_sc_address"`
	ProxyPublicKey                       string `json:"proxy_public_key"`
	SCAddress                            string `json:"sc_address"`
	State                                int    `json:"state"`
	TokensUsedProxyCommittedToBlockchain int64  `json:"tokens_used_proxy_committed_to_blockchain"`
	TokensUsedProxyCommittedToDB         int64  `json:"tokens_used_proxy_committed_to_db"`
	TokensUsedProxyMax                   int64  `json:"tokens_used_proxy_max"`
	TokensCharged                        int64  `json:"tokens_charged"`
	TokensPayed                          int64  `json:"tokens_payed"`
}

// StateSnapshot is a consistent copy of RunnerState for readers.
type StateSnapshot struct {
	WalletBalance    uint64
	TONLastSyncedAt  int64
	Enabled          bool
	GitCommit        string
	RootAddress      string
	OwnerAddress     string
	CheckImageHashes bool
	ProxyConnections []ProxyConnectionEntry
	Proxies          []ProxyStatEntry
}

// Snapshot returns a copy of the current state (slices copied).
func (s *RunnerState) Snapshot() StateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return StateSnapshot{
		WalletBalance:    s.WalletBalance,
		TONLastSyncedAt:  s.TONLastSyncedAt,
		Enabled:          s.Enabled,
		GitCommit:        s.GitCommit,
		RootAddress:      s.RootAddress,
		OwnerAddress:     s.OwnerAddress,
		CheckImageHashes: s.CheckImageHashes,
		ProxyConnections: append([]ProxyConnectionEntry(nil), s.ProxyConnections...),
		Proxies:          append([]ProxyStatEntry(nil), s.Proxies...),
	}
}

// HasReadySession reports whether any proxy connection is ready.
func (s *RunnerState) HasReadySession() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.ProxyConnections {
		if p.IsReady {
			return true
		}
	}
	return false
}

// SetProxyConnections atomically replaces the proxy_connections list.
func (s *RunnerState) SetProxyConnections(entries []ProxyConnectionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ProxyConnections = entries
}

// SetProxies atomically replaces the proxies list.
func (s *RunnerState) SetProxies(entries []ProxyStatEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Proxies = entries
}

// SetSyncTime updates ton_last_synced_at.
func (s *RunnerState) SetSyncTime(t int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TONLastSyncedAt = t
}

// SetWalletBalance updates the wallet balance shown in /jsonstats.
func (s *RunnerState) SetWalletBalance(b uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.WalletBalance = b
}

// SetIdentity updates the engine identity exposed in /jsonstats when the
// engine starts or stops.
func (s *RunnerState) SetIdentity(rootAddress, ownerAddress string, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RootAddress = rootAddress
	s.OwnerAddress = ownerAddress
	s.Enabled = enabled
}
