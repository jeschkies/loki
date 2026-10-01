package log

import (
	"fmt"
	"math/rand"
	"testing"
)

// BenchmarkMatchLo compares the AVX2 kernel (matchLo, dispatched via
// cpu.X86.HasAVX2 in matchlo_amd64.go's init) against the pure-Go
// matchLoGeneric it's differentially tested against - same inputs, same
// selectivity, varying batch size.
func BenchmarkMatchLo(b *testing.B) {
	r := rand.New(rand.NewSource(1))

	for _, n := range []int{64, 1024, 65536} {
		for _, sel := range []float64{1.0, 0.1, 0.0} {
			los := make([]uint64, n)
			const target = uint64(42)
			for i := range los {
				if r.Float64() < sel {
					los[i] = target
				} else {
					los[i] = target + 1
				}
			}
			mask := make([]byte, (n+7)/8)

			name := fmt.Sprintf("n=%d/selectivity=%.2f", n, sel)

			b.Run(name+"/Generic", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(n * 8))
				for range b.N {
					clear(mask)
					matchLoGeneric(los, target, mask)
				}
			})

			b.Run(name+"/AVX2", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(n * 8))
				for range b.N {
					clear(mask)
					matchLo(los, target, mask)
				}
			})
		}
	}
}
