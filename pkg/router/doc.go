// Package router implements the COCOON upstream proxy dialer used by pkg/cocoon.
// It handles TCP connect, the pre-TLS proof-of-work challenge, and the TLS 1.3
// mutual handshake expected by COCOON proxies.
package router
