package arrowfilter

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/dustin/go-humanize"
)

const needle = "matchme"

// buildBenchLines generates lines until their total size reaches size bytes.
// selectivity is the approximate fraction of lines that contain needle.
func buildBenchLines(size uint64, selectivity float64, seed int64) (lines []string, totalBytes uint64) {
	r := rand.New(rand.NewSource(seed))

	for i := 0; totalBytes < size; i++ {
		field := ""
		if r.Float64() < selectivity {
			field = needle
		}
		line := fmt.Sprintf("logline=%d level=info method=GET path=/api/v1/query duration=%dms extra=%s",
			i, r.Intn(1000), field)
		lines = append(lines, line)
		totalBytes += uint64(len(line))
	}
	return lines, totalBytes
}

func BenchmarkContains(b *testing.B) {
	sizes := []uint64{512 * humanize.KiByte, 1 * humanize.MiByte, 4 * humanize.MiByte}
	selectivities := []float64{1.0, 0.3, 0.1, 0.01, 0.0}

	for _, size := range sizes {
		for _, sel := range selectivities {
			lines, totalBytes := buildBenchLines(size, sel, 42)
			col := stringArray(b, lines)
			name := fmt.Sprintf("size=%s/selectivity=%.2f", humanize.Bytes(size), sel)

			b.Run(name+"/Naive", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(totalBytes))
				for range b.N {
					_ = ContainsNaive(col, []byte(needle), nil)
				}
			})

			b.Run(name+"/Scan", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(totalBytes))
				for range b.N {
					_ = Contains(col, []byte(needle), nil)
				}
			})

			b.Run(name+"/ScanSIMD", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(totalBytes))
				for range b.N {
					_ = ContainsSIMD(col, []byte(needle), nil)
				}
			})
		}
	}
}
