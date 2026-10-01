package log

// matchLo compares each of the first len(los) elements against target,
// setting bit i of mask[i/8] (LSB-first) for every match. mask must have
// at least (len(los)+7)/8 bytes and must be zeroed by the caller first -
// matchLo only ever sets bits, never clears them.
//
// This is PackedStringView's tier-1 scan (see FilterPackedStringViewSIMD):
// a cheap, uniform comparison of every row's Lo word (length+prefix)
// against a single target, producing a candidate bitmask before any
// row-specific branching (inline vs out-of-line) or buffer access.
// Overridden with an AVX2 implementation in matchlo_amd64.go when
// available; matchLoGeneric is both the portable fallback and the
// correctness oracle the AVX2 kernel is differentially tested against.
var matchLo = matchLoGeneric

func matchLoGeneric(los []uint64, target uint64, mask []byte) {
	for i, v := range los {
		if v == target {
			mask[i/8] |= 1 << (i % 8)
		}
	}
}
