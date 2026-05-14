# Development

## Prerequisites

- Go 1.25+
- Optional: `golangci-lint`, `staticcheck` (both run in CI; install for local parity)

## Commands

```bash
make build          # dist/gocoon and dist/gocoon-runner
make test           # full Go test suite with race detector and coverage
make vet            # go vet ./...
make lint           # vet plus golangci-lint when installed
make tidy           # go mod tidy
make build-cross    # linux, darwin, windows on amd64 and arm64
make build-android  # android/arm64
```

## Package Map

| Path | Purpose |
|---|---|
| `cmd/gocoon` | Standalone CLI. |
| `cmd/gocoon-runner` | Local runner and browser-compatible HTTP control plane. |
| `pkg/cocoon` | Public client API: sessions, inference, payments, discovery. |
| `pkg/contracts/root` | COCOON root contract reader and state parser. |
| `pkg/contracts/client` | COCOON client smart-contract messages. |
| `pkg/contracts/wallet` | COCOON node wallet generation, signing, and address derivation. |
| `pkg/router` | Direct proxy dialer: TCP, PoW, TLS 1.3, and COCOON proxy handshake transport. |
| `pkg/store` | Runner/client persistence abstraction. |
| `pkg/tl` | TL primitives and COCOON wire framing helpers. |
| `pkg/resources` | Embedded TON config and wallet code resources. |

## Browser Dev Loop

```bash
make install-browser BROWSER_REPO=../Tonnet-Browser-stable
```

Only the runner is installed under the browser's Telegram-compatible binary
name. `pkg/router` is linked into the runner and there is no separate router
process in the Go path.

## Coding Conventions

- Keep public IO APIs context-aware.
- Return typed or wrapped errors; do not panic in public paths.
- Use `slog` for runner logs.
- Prefer small tests next to the implementation.
- Keep generated or embedded upstream artifacts under `references/` or
  `pkg/resources/`.

## Release Checks

Before cutting a release:

```bash
make tidy
make vet
make test
make build-cross
make build-android
```
