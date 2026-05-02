package tl

import "errors"

// Sentinel errors returned by the Reader/Writer.
var (
	// ErrShortBuffer is returned when fewer bytes than required are available.
	ErrShortBuffer = errors.New("tl: short buffer")

	// ErrInvalidBool indicates the wire bytes do not match boolFalse or boolTrue.
	ErrInvalidBool = errors.New("tl: invalid bool constructor id")

	// ErrInvalidLength indicates a length prefix is malformed (e.g. 0xff is reserved).
	ErrInvalidLength = errors.New("tl: invalid length prefix")

	// ErrUnknownConstructor is returned when a constructor ID does not match any
	// registered type.
	ErrUnknownConstructor = errors.New("tl: unknown constructor id")
)
