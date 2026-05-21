// Package client wraps the cocoon_client smart contract: a per-(client,proxy)
// payment channel.
//
// Source: github.com/TelegramMessenger/cocoon-contracts/contracts/cocoon_client.fc
// Reference wrapper: wrappers/CocoonClient.ts (Apache-2.0).
package client

// Opcodes for cocoon_client SC, lifted from wrappers/CocoonClient.ts.
const (
	OpExtClientTopUp                      uint32 = 0xf172e6c2
	OpOwnerClientChangeSecretHashAndTopUp uint32 = 0x8473b408
	OpOwnerClientRegister                 uint32 = 0xc45f9f3b
	OpOwnerClientChangeSecretHash         uint32 = 0xa9357034
	OpOwnerClientIncreaseStake            uint32 = 0x6a1f6a60
	OpOwnerClientWithdraw                 uint32 = 0xda068e78
	OpOwnerClientRequestRefund            uint32 = 0xfafa6cc1
	OpChargeSigned                        uint32 = 0xbb63ff93
	OpGrantRefundSigned                   uint32 = 0xefd711e1
)
