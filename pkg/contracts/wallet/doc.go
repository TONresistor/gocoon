// Package wallet wraps the cocoon_wallet smart contract: a custom Ed25519
// wallet used as the cocoon-node identity. Distinct from the user's W4R2
// owner wallet.
//
// Source: github.com/TelegramMessenger/cocoon-contracts/contracts/cocoon_wallet.fc
// Reference wrapper: wrappers/CocoonWallet.ts (Apache-2.0).
//
// Parity goal: byte-for-byte equivalent address derivation and external
// message bodies vs. the browser's TS port at
// src/main/cocoon/contracts/wrappers/CocoonWallet.ts.
package wallet
