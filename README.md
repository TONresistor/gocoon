# gocoon

Pure-Go COCOON client. Run decentralized AI inference on TON without Telegram's
C++ runner build chain.

- Standalone CLI with an **OpenAI-compatible local HTTP API**
- Built-in local **wallet + chat UI** (`gocoon ui`) and desktop app
  (`gocoon-desktop`)
- OpenAI **function calling** support (`tools` array, `tool_calls` response)
- Full on-chain payment flow: stake → chat → close → withdraw
- Static cross-platform binaries (linux / darwin / windows × amd64 / arm64,
  android / arm64)

> **Mainnet only.** The full quick start spends ~20 TON of real mainnet funds
> to deploy the client smart contract and stake the channel. There is no
> testnet path today.

## Install

```bash
git clone https://github.com/TONresistor/gocoon.git
cd gocoon
make build
export PATH="$PWD/dist:$PATH"
```

`make build-desktop` produces a host desktop app binary (`gocoon-desktop.exe`
on Windows, `gocoon-desktop` on macOS/Linux) plus `gocoon-runner`.
`make build-cross` and `make build-android` produce release-grade static CLI
binaries for every supported target.

## Quick Start

For the local UI:

```bash
gocoon ui --dir ./gocoon-data
```

The UI creates the wallet/config bundle, shows the recovery phrase and full
backup JSON once, shows the funding address, starts the local runner, lists
models once a proxy session is ready, and provides a simple chat screen.

For a standalone desktop window instead of a browser tab:

```bash
gocoon-desktop
```

Keep `gocoon-runner` next to `gocoon-desktop`; the desktop app starts it when
you press **Connect to Cocoon**.

For CLI-only setup:

```bash
gocoon init --dir ./gocoon-data
```

`init` prints a `fund_address` and a recommended amount. Send the TON, then:

```bash
gocoon wallet wait-funded \
  --wallet ./gocoon-data/wallet.json \
  --config ./gocoon-data/client-config.json

gocoon run --config ./gocoon-data/client-config.json
```

The runner blocks. In a second terminal:

```bash
gocoon chat "Reply in one short sentence."
```

Or hit the OpenAI-compatible endpoint directly:

```bash
curl http://127.0.0.1:10000/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"default","messages":[{"role":"user","content":"hi"}]}'
```

When you are done, recover the funds:

```bash
gocoon channel close
gocoon wallet withdraw \
  --wallet ./gocoon-data/wallet.json \
  --config ./gocoon-data/client-config.json \
  --to <YOUR_TON_ADDRESS>
```

## What's Inside

| Path                | Role                                                     |
| ------------------- | -------------------------------------------------------- |
| `cmd/gocoon`        | Standalone CLI and local web UI                          |
| `cmd/gocoon-runner` | Local runner, HTTP control plane on `127.0.0.1:10000`    |
| `pkg/cocoon`        | Client library: sessions, inference, payments, discovery |
| `pkg/contracts`     | COCOON root / client / wallet contract helpers           |
| `pkg/router`        | Direct proxy dialer: TCP, PoW, TLS 1.3                   |
| `pkg/tl`            | TL primitives and wire framing                           |
| `pkg/store`         | Persistence (memory and bbolt backends)                  |

## Documentation

- [Quick start](docs/quickstart.md) full walk-through
- [CLI reference](docs/cli.md) every command and flag
- [Security notes](docs/security.md) wallet handling, RA-TLS posture
  
## Development

```bash
make test          # tests with race detector and coverage
make vet           # static analysis
make build-cross   # release-grade cross-builds
```

See [DEVELOPMENT.md](DEVELOPMENT.md) for the full contributor workflow.

## License

Apache-2.0. Copyright 2026 Digital Resistance.

This repository implements compatibility with the public COCOON protocol and
smart-contract interfaces published by Telegram FZ-LLC. See [NOTICE](NOTICE)
for upstream attributions. gocoon is not affiliated with or endorsed by
Telegram.
