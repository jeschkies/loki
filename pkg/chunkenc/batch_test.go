package chunkenc

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/grafana/loki/pkg/chunkenc/testdata"
	"github.com/grafana/loki/pkg/logproto"
)

func BenchmarkFilter(b *testing.B) {
	entries := make([]logproto.Entry, 0)
	batch := &batch{}
	r := rand.New(rand.NewSource(42))

	sizes := []uint64{256 * humanize.KiByte, 512 * humanize.KiByte, 1 * humanize.MiByte, 4 * humanize.MiByte}
	selectivity := []float64{0.3, 0.1, 0.01}
	for _, sel := range selectivity {
		for _, size := range sizes {
			totalBytes := uint64(0)
			needle := "matchme"
			field := "" 
			if r.Float64() < sel {
				field = "matchme"
			}
			for i := int64(0); totalBytes < size; i++ {

				line := fmt.Sprintf("%s select=%s", testdata.LogString(i), field)
				entries = append(entries, logproto.Entry{
					Timestamp: time.Unix(i, 0),
					Line:      line,
				})
				batch.Append(i, []byte(line))
				totalBytes += uint64(len(line))
			}

			b.ResetTimer()
			b.ReportAllocs()
			b.Run(fmt.Sprintf("sequential-%s-%f", humanize.Bytes(size), sel), func(b *testing.B) {
				for n := 0; n < b.N; n++ {
					for _, e := range entries {
						strings.Contains(e.Line, needle)
					}
					b.SetBytes(int64(totalBytes))
				}
			})

			b.ResetTimer()
			b.Run(fmt.Sprintf("batch-%s-%f", humanize.Bytes(size), sel), func(b *testing.B) {
				haystack := batch.lines
				for n := 0; n < b.N; n++ {
					i := 0
					for i < len(batch.offsets) {
						start := batch.offsets[i]
						offset := bytes.Index(haystack[start:], []byte(needle))
						if offset == -1 {
							break
						}

						// Find first start that is above pos.
						// TODO: We can probably be branchless here.
						for i < len(batch.offsets) && batch.offsets[i] <= start+int64(offset) {
							i++
						}
					}
					b.SetBytes(int64(totalBytes))
				}
			})
		}
	}
}
