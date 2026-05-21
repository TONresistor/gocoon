# Changelog

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

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

[Unreleased]: https://github.com/TONresistor/gocoon/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/TONresistor/gocoon/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/TONresistor/gocoon/releases/tag/v0.1.0
