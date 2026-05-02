package tl

import "math"

// Indirection so we don't import math from primitives.go (clearer separation).
func float64ToBits(v float64) uint64   { return math.Float64bits(v) }
func float64FromBits(u uint64) float64 { return math.Float64frombits(u) }
