package resources

import _ "embed"

// CocoonWalletCodeJSON is the compiled cocoon_wallet smart-contract code
// resource used by wallet generation and cocoon_wallet deployment.
//
//go:embed cocoon/cocoon-wallet.code.json
var CocoonWalletCodeJSON []byte

// TONConfigJSON is the TON mainnet global config used by standalone runner
// configs when the caller does not provide a custom config.
//
//go:embed cocoon/ton-config.json
var TONConfigJSON []byte
