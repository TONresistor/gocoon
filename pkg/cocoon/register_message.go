package cocoon

import (
	clientcontract "github.com/TONresistor/gocoon/pkg/contracts/client"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// buildProxyRegisterMessage produces the owner_client_register body that
// ClientProxyConnection.cpp's long-auth path broadcasts on-chain via the
// cocoon_wallet -> derived client SC, with a nonce supplied by the proxy in the
// connectedToProxy auth challenge.
//
// Source upstream: ClientProxyConnection.cpp::authorize_long calls
// proxy->sc()->create_proxy_register_message(nonce), where proxy->sc() is the
// derived client contract wrapper.
//
// Layout:
//
//	body = op:0xc45f9f3b (32) | query_id (64) | nonce (64) | send_excesses_to
func buildProxyRegisterMessage(nonce uint64, sendExcessesTo *address.Address) (*cell.Cell, error) {
	return clientcontract.BuildOwnerRegister(nonce, sendExcessesTo)
}
