# CLI Reference

## Setup

```bash
gocoon init --dir ./gocoon-data [--json] [--force]
gocoon config generate --wallet wallet.json --ton-config global.config.json [--out client-config.json]
```

## Wallet

```bash
gocoon wallet generate [--pretty]
gocoon wallet info --wallet wallet.json [--config client-config.json]
gocoon wallet wait-funded --wallet wallet.json --config client-config.json
gocoon wallet withdraw --wallet wallet.json --config client-config.json --to <TON_ADDRESS>
```

`wallet withdraw` sends all remaining TON from the Cocoon node wallet using TON
send mode `160` (`128 + 32`): carry all remaining balance and destroy if zero.

## Runner

```bash
gocoon run --config client-config.json [--runner ./gocoon-runner] [-v 3]
gocoon status
gocoon models
gocoon chat --model Qwen/Qwen3-32B --prompt "Hello"
gocoon serve
```

By default the CLI talks to the local runner at `http://127.0.0.1:10000`.

## Local UI

```bash
gocoon ui --dir ./gocoon-data [--runner ./gocoon-runner] [--addr 127.0.0.1:17770]
gocoon ui --window --dir ./gocoon-data
```

The UI serves a local browser app. It can create the Cocoon wallet/config
bundle, show the recovery phrase and full backup JSON once, show the funding
address and on-chain balance, start/stop the local runner, list models, and
send chat completions through the runner.

For a desktop binary, build with the `desktop` tag or use:

```bash
make build-desktop
```

The produced `gocoon-desktop` executable opens the same UI in a native app
window. On Windows the build uses WebView2 and hides the console window.

## Channel

```bash
gocoon channel info --config client-config.json
gocoon channel topup --amount <nanoTON>
gocoon channel withdraw
gocoon channel close
```

Channel commands call the local runner HTTP control plane.

## Root Contract

```bash
gocoon root --config client-config.json
gocoon root --ton-config global.config.json
```

Outputs root contract parameters as JSON.
