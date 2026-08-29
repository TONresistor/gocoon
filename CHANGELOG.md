# Changelog

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed

- Raise the minimum supported Go version from 1.25.5 to 1.26.0.
- Bump tonutils-go from v1.17.2 to v1.18.0 (and indirect
  github.com/pierrec/lz4/v4 v4.1.27, golang.org/x/crypto v0.54.0,
  golang.org/x/sys v0.47.0).

## [0.2.1] - 2026-06-20

### Changed

- Bump tonutils-go to v1.17.2 (and indirect golang.org/x/crypto v0.53.0,
  golang.org/x/sys v0.46.0).

## [0.2.0] - 2026-05-21

### Added

- OpenAI tool-calling: `tools` requests are rewritten to a tag-based prompt;
  `<tool_call>` blocks in the response become an OpenAI `tool_calls` array.
- Multi-part `content` support (string or array of text parts) on system,
  assistant, and tool messages.
- Transport keepalive: `tcp.ping` every 10s to keep the proxy connection open.
- Telegram CI notification on push to `main` / `dev`.
- Reusable `_test.yml` workflow, called from `ci.yml` and `release.yml`.

### Changed

- CI: `if-no-files-found: error` on all `upload-artifact` steps.
- Makefile: `mkdir -p` before `build`; `set -e` in cross-build loops.

### Removed

- Streaming. `stream:true` is force-disabled in the request body; all
  responses come back as one JSON document.

## [0.1.0] - 2026-05-02

### Added

- Initial release: standalone CLI, local runner with OpenAI-compatible HTTP
  endpoint, on-chain payment flow, cross-platform binaries.

[Unreleased]: https://github.com/TONresistor/gocoon/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/TONresistor/gocoon/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/TONresistor/gocoon/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/TONresistor/gocoon/releases/tag/v0.1.0
