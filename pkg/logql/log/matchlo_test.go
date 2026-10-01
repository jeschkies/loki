package log

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchLo_AgreesWithGeneric(t *testing.T) {
	r := rand.New(rand.NewSource(1))

	for _, n := range []int{0, 1, 3, 7, 8, 9, 15, 16, 17, 31, 32, 33, 100, 4096} {
		los := make([]uint64, n)
		// A small value universe so both targets-that-match and
		// targets-that-don't are well exercised.
		for i := range los {
			los[i] = uint64(r.Intn(8))
		}

		for target := uint64(0); target < 9; target++ {
			want := make([]byte, (n+7)/8)
			matchLoGeneric(los, target, want)

			got := make([]byte, (n+7)/8)
			matchLo(los, target, got)

			require.Equal(t, want, got, "n=%d target=%d", n, target)
		}
	}
}

func TestMatchLo_ExactBoundary(t *testing.T) {
	// Values exactly at and around an 8-element group boundary, to
	// catch off-by-one errors in the AVX2 loop's group handling or the
	// scalar tail's start index.
	los := make([]uint64, 17)
	los[7] = 42
	los[8] = 42
	los[16] = 42

	mask := make([]byte, (len(los)+7)/8)
	matchLo(los, 42, mask)

	want := make([]byte, (len(los)+7)/8)
	matchLoGeneric(los, 42, want)
	require.Equal(t, want, mask)

	for i, v := range los {
		bit := mask[i/8]&(1<<(i%8)) != 0
		require.Equal(t, v == 42, bit, "index %d", i)
	}
}
