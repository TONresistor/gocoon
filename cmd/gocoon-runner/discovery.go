package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/TONresistor/gocoon/pkg/cocoon"
	"github.com/TONresistor/gocoon/pkg/contracts/root"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/ton"
)

// runDiscovery is the background loop that:
//
//  1. Fetches the registered proxy list from cocoon_root.
//  2. Fetches the wallet balance for /jsonstats.
//  3. Fetches root config version used by client.connectToProxy.
//  4. Asynchronously attempts a real session against the first proxy. The
//     runner only reports readiness after the proxy session is actually ready.
func runDiscovery(ctx context.Context, cli *cocoon.Client, api ton.APIClientWrapped, walletAddr *address.Address, state *RunnerState, logger *slog.Logger) {
	// Initial population so /jsonstats has something the browser can parse.
	state.SetSyncTime(time.Now().Unix())

	// Fetch wallet balance.
	go pollWalletBalance(ctx, api, walletAddr, state, logger)

	// Read root contract for registered proxies (read-only, no state mutation).
	rdr, err := root.NewReader(api, cli.RootAddressString())
	if err != nil {
		logger.Warn("discovery: build root reader", "err", err)
	} else {
		if summary, err := rdr.GetSummary(ctx); err != nil {
			logger.Warn("discovery: root summary", "err", err)
		} else {
			cli.SetRootConfigVersion(int32(summary.Version))
			logger.Info("discovery: root config",
				"version", summary.Version,
				"params_version", summary.ParamsVersion,
			)
		}

		proxies, err := rdr.RegisteredProxies(ctx)
		if err != nil {
			logger.Warn("discovery: registered proxies", "err", err)
		} else {
			logger.Info("discovery: proxies in root", "count", len(proxies))
		}
	}

	// Try a real session against the first known proxy in the background.
	// state.ProxyConnections / state.Proxies stay empty until a session is
	// actually Ready , the previous "optimistic" population corrupted the
	// browser's stake-cache by writing the cocoon-wallet address as the
	// client_sc address. We never lie about readiness now.
	go tryConnectFirstProxy(ctx, cli, state, logger)
}

func tryConnectFirstProxy(ctx context.Context, cli *cocoon.Client, state *RunnerState, logger *slog.Logger) {
	procs, err := cli.RegisteredProxies(ctx)
	if err != nil || len(procs) == 0 {
		return
	}
	first := procs[0]
	endpoints := splitEndpoints(first.NetworkAddr)
	if len(endpoints) == 0 {
		logger.Warn("discovery: no parseable endpoints", "raw", first.NetworkAddr)
		return
	}
	attempt := 0
	for {
		attempt++
		for _, ep := range endpoints {
			logger.Info("discovery: attempting live session", "proxy", ep, "attempt", attempt)
			sess, err := cli.ConnectProxy(ctx, ep)
			if err != nil {
				logger.Warn("discovery: live session failed", "endpoint", ep, "attempt", attempt, "err", err)
				if isTerminalClientSessionError(err) {
					logger.Info("discovery: stopping live-session retry for terminal client state", "endpoint", ep)
					state.SetProxyConnections(nil)
					state.SetProxies(nil)
					return
				}
				continue
			}
			logger.Info("discovery: live session ready",
				"proxy", ep,
				"client_sc", sess.ClientSCAddress(),
				"proxy_sc", sess.Params().SCAddress,
			)
			// Populate /jsonstats with the REAL addresses returned by the proxy.
			// This is the only path that flips is_ready=true honestly.
			state.SetProxyConnections([]ProxyConnectionEntry{{
				Address:        ep,
				IsReady:        true,
				ProxySCAddress: sess.Params().SCAddress,
			}})
			state.SetProxies([]ProxyStatEntry{{
				ProxySCAddress:     sess.Params().SCAddress,
				SCAddress:          sess.ClientSCAddress(),
				State:              0,
				TokensUsedProxyMax: 1_000_000,
			}})
			waitSessionReadyOrContext(ctx, sess)
			logger.Warn("discovery: live session lost", "proxy", ep, "status", sess.Status())
			state.SetProxyConnections(nil)
			state.SetProxies(nil)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func isTerminalClientSessionError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "client is closing") || strings.Contains(msg, "client is closed")
}

func waitSessionReadyOrContext(ctx context.Context, sess *cocoon.Session) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if sess.Status() != cocoon.SessionReady {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// splitEndpoints parses a registered_proxies value, which upstream encodes as:
//
//	"<workers_addr>"                        (single addr → both workers & clients are this)
//	"<workers_addr> <clients_addr>"         (split: workers first, clients second)
//
// Source: TelegramMessenger/cocoon RootContractConfig.cpp:249-263.
//
// We are a CLIENT, so we return ONLY the clients_addr. Hitting the workers
// port produces the dead-end error code 621 "client connect() from non
// client" because the server allocates a worker connection object, then the
// client.connectToProxy dispatch sees a non-client connection and rejects.
func splitEndpoints(raw string) []string {
	tokens := []string{}
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == ' ' || raw[i] == '\t' {
			if i > start {
				tok := raw[start:i]
				if tok != "" {
					tokens = append(tokens, tok)
				}
			}
			start = i + 1
		}
	}
	switch len(tokens) {
	case 0:
		return nil
	case 1:
		return tokens // workers == clients
	default:
		// Format: "workers clients [...]" , return the clients endpoint.
		return tokens[1:]
	}
}

// pollWalletBalance updates state.WalletBalance every 30s.
func pollWalletBalance(ctx context.Context, api ton.APIClientWrapped, walletAddr *address.Address, state *RunnerState, logger *slog.Logger) {
	for {
		mc, err := api.CurrentMasterchainInfo(ctx)
		if err != nil {
			logger.Debug("discovery: masterchain info", "err", err)
		} else {
			acc, err := api.GetAccount(ctx, mc, walletAddr)
			if err != nil {
				logger.Debug("discovery: get account", "err", err)
			} else if acc != nil && acc.IsActive && acc.State != nil {
				state.SetWalletBalance(acc.State.Balance.Nano().Uint64())
			}
		}
		state.SetSyncTime(time.Now().Unix())
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}
