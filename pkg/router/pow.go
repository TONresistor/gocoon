package router

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
)

// PoW pre-TLS challenge/response, mirrors upstream tee/cocoon/pow.{h,cpp}.
//
// Wire format:
//
//	Server → Client: [magic:u32 LE = 0x418e1291][difficulty_bits:i32 LE][salt:16 raw bytes]   = 24 bytes
//	Client → Server: [magic:u32 LE = 0x01827319][nonce:i64 LE]                                 = 12 bytes
//
// Verification:
//   hash = SHA256(salt || nonce_le_8_bytes)
//   take the first 8 bytes of hash as uint64 little-endian
//   leading_zeros_64(value) >= difficulty_bits
//
// This LE interpretation matches what upstream's `td::as<uint64>(hash.raw)`
// produces on x86_64 / aarch64 (both little-endian). If a future proxy uses a
// different convention we'll need to flip to BE, but the C++ source path is
// LE on every supported platform.

const (
	powMagicChallenge uint32 = 0x418e1291
	powMagicResponse  uint32 = 0x01827319

	// MaxDifficulty caps how much CPU we burn solving a hostile/buggy
	// challenge. 32 bits = a few seconds on modern hardware, more would
	// be a DoS on us.
	MaxDifficulty int32 = 32
)

// ErrPoWMagicMismatch indicates the challenge bytes did not start with the
// expected magic. Likely the peer doesn't speak PoW.
var ErrPoWMagicMismatch = errors.New("router: pow: challenge magic mismatch")

// ErrPoWDifficultyTooHigh is returned when the server requests more leading
// zero bits than MaxDifficulty allows.
var ErrPoWDifficultyTooHigh = errors.New("router: pow: difficulty exceeds MaxDifficulty")

// SolvePoW reads the 24-byte challenge from rw, computes a valid nonce, and
// writes the 12-byte response. Returns nil on success.
//
// If the connection does not speak PoW (e.g. an upstream that opted to skip
// it), the caller can detect this by inspecting whether the first 4 bytes
// match `powMagicChallenge`. If not, the bytes already consumed must be
// preserved for the next layer; for that reason we use a peek-style read
// via Peeker.
func SolvePoW(rw io.ReadWriter, maxDifficulty int32) error {
	if maxDifficulty <= 0 {
		maxDifficulty = MaxDifficulty
	}
	var challenge [24]byte
	if _, err := io.ReadFull(rw, challenge[:]); err != nil {
		return fmt.Errorf("router: pow: read challenge: %w", err)
	}
	magic := binary.LittleEndian.Uint32(challenge[0:4])
	if magic != powMagicChallenge {
		return fmt.Errorf("%w: got %#x", ErrPoWMagicMismatch, magic)
	}
	difficulty := int32(binary.LittleEndian.Uint32(challenge[4:8]))
	if difficulty < 0 || difficulty > maxDifficulty {
		return fmt.Errorf("%w: server asked %d, max %d", ErrPoWDifficultyTooHigh, difficulty, maxDifficulty)
	}
	var salt [16]byte
	copy(salt[:], challenge[8:24])

	nonce, err := findNonce(salt, difficulty)
	if err != nil {
		return err
	}

	var resp [12]byte
	binary.LittleEndian.PutUint32(resp[0:4], powMagicResponse)
	binary.LittleEndian.PutUint64(resp[4:12], uint64(nonce))
	if _, err := rw.Write(resp[:]); err != nil {
		return fmt.Errorf("router: pow: write response: %w", err)
	}
	return nil
}

// findNonce brute-forces a nonce such that leading_zero_bits(SHA256(salt||nonce)[0:8] as LE uint64) >= difficulty.
func findNonce(salt [16]byte, difficulty int32) (int64, error) {
	if difficulty == 0 {
		// Any nonce works.
		return 0, nil
	}
	var data [24]byte
	copy(data[:16], salt[:])
	for nonce := int64(0); nonce >= 0; nonce++ {
		binary.LittleEndian.PutUint64(data[16:24], uint64(nonce))
		h := sha256.Sum256(data[:])
		v := binary.LittleEndian.Uint64(h[:8])
		if int32(bits.LeadingZeros64(v)) >= difficulty {
			return nonce, nil
		}
		// We don't yield to a scheduler here because Go runtime preempts
		// goroutines automatically.
	}
	return 0, errors.New("router: pow: nonce overflow (impossible difficulty)")
}

// VerifyPoWResponse is the server-side counterpart, exposed for tests.
// Given the salt and nonce, returns true iff the hash satisfies the difficulty.
func VerifyPoWResponse(salt [16]byte, difficulty int32, nonce int64) bool {
	var data [24]byte
	copy(data[:16], salt[:])
	binary.LittleEndian.PutUint64(data[16:24], uint64(nonce))
	h := sha256.Sum256(data[:])
	v := binary.LittleEndian.Uint64(h[:8])
	return int32(bits.LeadingZeros64(v)) >= difficulty
}
