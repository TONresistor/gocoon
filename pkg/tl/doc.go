// Package tl implements the Telegram TL (Type Language) wire format used by
// the COCOON network protocol.
//
// The schema is published at TelegramMessenger/cocoon/tl/generate/scheme/cocoon_api.tl
// and vendored in this repo at references/cocoon_api.tl.
//
// This package provides the primitive Reader/Writer for TL types. The
// higher-level constructors currently used by gocoon are maintained by hand in
// this package.
//
// Wire conventions (matching the standard TL spec):
//
//   - All integers little-endian.
//   - int   = 4 bytes
//   - long  = 8 bytes
//   - int128= 16 bytes (raw)
//   - int256= 32 bytes (raw)
//   - bytes = length-prefixed blob with 4-byte alignment padding
//   - string= same encoding as bytes, UTF-8 expected
//   - vector= 4-byte count + count * t serialization
//   - bool  = constructor IDs boolFalse=0xbc799737, boolTrue=0x997275b5
//   - flags = 4-byte bitmask gating optional fields
//   - constructors are prefixed by their 4-byte ID
//
// Bytes/string length encoding:
//
//   - len < 254: single byte len, then len bytes, then padding to 4-byte align
//   - len >= 254: byte 0xfe, then 3-byte little-endian length, then bytes,
//     then padding to 4-byte align
package tl
