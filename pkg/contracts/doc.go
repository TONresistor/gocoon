// Package contracts contains Go ports of the COCOON smart-contract wrappers
// originally published in TypeScript at TelegramMessenger/cocoon-contracts.
//
// Subpackages mirror the upstream split:
//
//   - root   : cocoon_root  (registry; read-only for clients)
//   - client : cocoon_client (per-(client,proxy) payment channel)
//   - wallet : cocoon_wallet (custom wallet used as cocoon-node identity)
//
// All wrappers target mainnet only.
package contracts
