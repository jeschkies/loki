package chunkenc

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTimestampsDoD_RoundTrip(t *testing.T) {
	base := time.Now().UnixNano()
	cases := map[string][]int64{
		"empty":         {},
		"single":        {base},
		"two":           {base, base + 1000},
		"constant step": genTimestamps(base, 1000, 1000, 0),
		"jittered step": genTimestamps(base, 1000, 500, 200),
		"large jumps":   genTimestamps(base, 50, 1_000_000_000, 900_000_000),
		"negative delta (out of order)": {
			base, base + 5000, base + 2000, base + 100000, base - 50000,
		},
		"zero and negative timestamps": {0, -1000, -500, 0, 1000},
		"repeated timestamps":          {base, base, base, base + 1, base + 1},
	}

	for name, ts := range cases {
		t.Run(name, func(t *testing.T) {
			encoded := encodeTimestampsDoD(ts)
			decoded := decodeTimestampsDoD(encoded, len(ts), nil)
			require.Equal(t, ts, decoded)
		})
	}
}

func TestTimestampsDoD_RoundTrip_Fuzz(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for trial := 0; trial < 200; trial++ {
		n := r.Intn(200)
		ts := make([]int64, n)
		cur := r.Int63()
		for i := range ts {
			// Mix of small steps, occasional big jumps, and occasional
			// exact repeats/negative deltas, to exercise every size class.
			switch r.Intn(4) {
			case 0:
				cur += int64(r.Intn(2000))
			case 1:
				cur += r.Int63n(1 << 40)
			case 2:
				// no change
			case 3:
				cur -= int64(r.Intn(2000))
			}
			ts[i] = cur
		}

		encoded := encodeTimestampsDoD(ts)
		decoded := decodeTimestampsDoD(encoded, n, nil)
		require.Equal(t, ts, decoded, "trial %d, n=%d", trial, n)
	}
}

// genTimestamps builds n timestamps starting at base, each step apart plus
// a random jitter in [-jitter, jitter].
func genTimestamps(base int64, n int, step, jitter int64) []int64 {
	r := rand.New(rand.NewSource(1))
	out := make([]int64, n)
	cur := base
	for i := range out {
		out[i] = cur
		delta := step
		if jitter > 0 {
			delta += r.Int63n(2*jitter+1) - jitter
		}
		cur += delta
	}
	return out
}
