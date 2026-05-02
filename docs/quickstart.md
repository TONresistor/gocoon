# Quick Start

This guide runs gocoon as a standalone COCOON client.

## Build

```bash
make build
```

## Initialize

```bash
./dist/gocoon init --dir ./gocoon-data
```

The command writes:

- `gocoon-data/wallet.json`
- `gocoon-data/client-config.json`
- `gocoon-data/ton-config.json`

`wallet.json` contains secrets. Do not commit or share it.

## Fund

Send the recommended TON amount to the printed `fund_address`, then wait for
confirmation:

```bash
./dist/gocoon wallet wait-funded \
  --wallet ./gocoon-data/wallet.json \
  --config ./gocoon-data/client-config.json
```

## Run

```bash
./dist/gocoon run \
  --config ./gocoon-data/client-config.json \
  --runner ./dist/gocoon-runner
```

## Use

In another terminal:

```bash
./dist/gocoon models
./dist/gocoon chat --model Qwen/Qwen3-32B --prompt "Reply in one short sentence."
```

## Close And Withdraw

Close the client channel:

```bash
./dist/gocoon channel close
```

After funds return to the Cocoon wallet, withdraw them to a regular TON wallet:

```bash
./dist/gocoon wallet withdraw \
  --wallet ./gocoon-data/wallet.json \
  --config ./gocoon-data/client-config.json \
  --to <TON_ADDRESS>
```
