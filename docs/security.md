# Security Notes

## Wallet Material

`gocoon init` and `gocoon wallet generate` produce wallet material that can
control TON funds. Never commit or share generated wallet files.

Sensitive fields include:

- `ownerMnemonic`
- `nodeSecretBase64`
- `node_wallet_key`
- `secret_string`

## Withdrawal

`gocoon wallet withdraw` drains the Cocoon node wallet to a destination TON
address. Check the destination carefully before running the command.

## Attestation

This version uses permissive proxy TLS validation. It does not enforce strict
TDX/RA-TLS quote validation. That is a future hardening item.

## Binaries

For published releases, prefer tagged GitHub release artifacts and verify
checksums from `SHA256SUMS.txt`.
