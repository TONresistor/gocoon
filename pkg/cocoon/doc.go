// Package cocoon is the public client API for the COCOON decentralized AI
// inference network on TON.
//
// It provides a [Client] type that handles proxy discovery via the on-chain
// root contract, TLS sessions to upstream proxies, inference request streaming,
// and payment channel lifecycle.
//
// This implementation is permissive: it accepts upstream proxy RA-TLS
// attestations without independently verifying enclave measurements.
package cocoon
