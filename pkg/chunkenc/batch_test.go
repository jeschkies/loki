package chunkenc

import (
	//"bytes"
	"fmt"
	"math/rand"

	//"strings"
	"testing"
	"time"

	"github.com/dustin/go-humanize"
	//"github.com/golang/snappy"
	memmem "github.com/jeschkies/go-memmem/pkg/search"

	"github.com/pierrec/lz4/v4"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/v3/pkg/chunkenc/testdata"
	"github.com/grafana/loki/v3/pkg/logproto"
)

func BenchmarkFilter(b *testing.B) {
	entries := make([]logproto.Entry, 0)
	batch := &batch{}
	r := rand.New(rand.NewSource(42))

	sizes := []uint64{256 * humanize.KiByte, 512 * humanize.KiByte, 1 * humanize.MiByte, 4 * humanize.MiByte}
	selectivity := []float64{1.0, 0.3, 0.1, 0.01}
	for _, sel := range selectivity {
		for _, size := range sizes {
			totalBytes := uint64(0)
			needle := "matchme"
			needleB := []byte(needle)
			field := ""
			// TODO: this can be improved. With too few samples we might not have any match.
			if r.Float64() < sel {
				field = needle
			}
			for i := int64(0); totalBytes < size; i++ {
				line := fmt.Sprintf("%s needle=%s", testdata.LogString(i), field)
				entries = append(entries, logproto.Entry{
					Timestamp: time.Unix(i, 0),
					Line:      line,
				})
				batch.Append(i, []byte(line))
				totalBytes += uint64(len(line))
			}

			b.ResetTimer()
			b.ReportAllocs()
			/*
				b.Run(fmt.Sprintf("sequential-%s-%f", humanize.Bytes(size), sel), func(b *testing.B) {
					for n := 0; n < b.N; n++ {
						for _, e := range entries {
							strings.Contains(e.Line, needle)
						}
						b.SetBytes(int64(totalBytes))
					}
				})
			*/

			b.ResetTimer()
			b.Run(fmt.Sprintf("%s-%f", humanize.Bytes(size), sel), func(b *testing.B) {
				haystack := batch.lines
				var offset int64
				for n := 0; n < b.N; n++ {
					i := 0
					for i < len(batch.offsets) {
						start := batch.offsets[i]
						//offset := bytes.Index(haystack[start:], []byte(needle))
						offset = memmem.Index(haystack[start:], needleB)
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

func BenchmarkCompressionThreshold(b *testing.B) {
	sizes := []uint64{256 * humanize.KiByte, 512 * humanize.KiByte, 1 * humanize.MiByte, 4 * humanize.MiByte, 16 * humanize.MiByte}
	for _, size := range sizes {
		totalBytes := uint64(0)
		batch := make([]byte, 0)
		for i := int64(0); totalBytes < size; i++ {
			line := testdata.LogString(i)
			batch = append(batch, []byte(line)...)
			totalBytes += uint64(len(line))
		}

		//var dst bytes.Buffer
		//dst.Grow(len(batch))
		dst := make([]byte, len(batch))
		c := &lz4.Compressor{}
		// TODO: try https://pkg.go.dev/github.com/pierrec/lz4/v4#Compressor
		//w := lz4.NewWriter(&dst)
		//w.Apply(lz4.BlockSizeOption(bs))
		//w := snappy.NewWriter(&dst)

		b.ResetTimer()
		b.ReportAllocs()
		b.Run(humanize.Bytes(size), func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				_, err := c.CompressBlock(batch, dst)
				require.NoError(b, err)
				b.SetBytes(int64(totalBytes))
			}
		})
	}
}
