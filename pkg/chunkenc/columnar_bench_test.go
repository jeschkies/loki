package chunkenc

import (
	"context"
	"fmt"
	"testing"

	"github.com/dustin/go-humanize"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/v3/pkg/compression"
	"github.com/grafana/loki/v3/pkg/logql/log"
	"github.com/grafana/loki/v3/pkg/logqlmodel/stats"
)

// v4BlockSize is the production default (chunk_block_size) - existing V4
// chunks in the wild were written at this size and wouldn't be
// retroactively rewritten by adopting a new format. newFormatBlockSize is
// what V5 is compared at instead: a new format is free to pick its own
// block-size tuning, independent of what V4 already committed to on disk.
// Confirmed via direct measurement (256KB->512KB gave V5 a substantial
// lift by amortizing fixed per-block decode overhead; 512KB->1MiB gave
// almost nothing further) that 512KB is close to where that curve
// flattens out, rather than an arbitrary choice.
const (
	v4BlockSize        = 256 * 1024
	newFormatBlockSize = 512 * 1024
)

// BenchmarkChunkFormat_V4VsV5 isolates the wire format's effect on decode,
// holding everything else fixed: both legs go through the same Batch
// (ArrowBatch/ProcessBatch) consumption path added earlier on this branch,
// with the same production stdlib-backed containsBatch scan and the same
// two-filter chain, on identical underlying data - except block size,
// deliberately (see v4BlockSize/newFormatBlockSize). Only the on-disk
// block layout differs otherwise (V4's row-major moveNext decode vs V5's
// columnar decode).
func BenchmarkChunkFormat_V4VsV5(b *testing.B) {
	sizes := []uint64{256 * humanize.KiByte, 1 * humanize.MiByte, 4 * humanize.MiByte}
	selectivities := []float64{1.0, 0.1, 0.0}
	const enc = compression.LZ4_256k

	for _, size := range sizes {
		for _, sel := range selectivities {
			c4, totalBytes := buildBenchChunkFmt(b, ChunkFormatV4, UnorderedWithStructuredMetadataHeadBlockFmt, enc, v4BlockSize, size, sel, 42)
			c5, _ := buildBenchChunkFmt(b, ChunkFormatV5, UnorderedWithColumnarHeadBlockFmt, enc, newFormatBlockSize, size, sel, 42)

			stages := benchFilterStages(b)
			pipeline := log.NewPipeline(stages).ForStream(labels.FromStrings("app", "bench"))

			name := fmt.Sprintf("size=%s/selectivity=%.2f", humanize.Bytes(size), sel)

			b.Run(name+"/V4-Batch", func(b *testing.B) {
				_, ctx := stats.NewContext(context.Background())
				b.ReportAllocs()
				b.SetBytes(int64(totalBytes))
				for range b.N {
					drainNewEntries(ctx, c4, pipeline)
				}
			})

			b.Run(name+"/V5-Batch", func(b *testing.B) {
				_, ctx := stats.NewContext(context.Background())
				b.ReportAllocs()
				b.SetBytes(int64(totalBytes))
				for range b.N {
					drainNewEntries(ctx, c5, pipeline)
				}
			})
		}
	}
}

// BenchmarkEndToEnd_V5SIMDBatch is the bottom-line comparison: everything
// this branch changed, stacked together (ChunkFormatV5's columnar decode +
// containsBatch's scan + a StreamSampleExtractor's batch count_over_time/
// bytes_over_time), against the original baseline (ChunkFormatV4's
// row-major moveNext decode, Process called once per line). The scan
// implementation (stdlib vs SIMD) is whatever pkg/logql/log/batch.go's
// containsBatch currently uses - see that file's comment for how to swap
// it to arrowfilter.ContainsSIMD for a run, temporarily, then revert.
func BenchmarkEndToEnd_V5SIMDBatch(b *testing.B) {
	sizes := []uint64{256 * humanize.KiByte, 1 * humanize.MiByte, 4 * humanize.MiByte}
	selectivities := []float64{1.0, 0.1, 0.0}
	const enc = compression.LZ4_256k

	for _, ex := range []struct {
		name string
		fn   log.LineExtractor
	}{
		{"count_over_time", log.CountExtractor},
		{"bytes_over_time", log.BytesExtractor},
	} {
		for _, size := range sizes {
			for _, sel := range selectivities {
				c4, totalBytes := buildBenchChunkFmt(b, ChunkFormatV4, UnorderedWithStructuredMetadataHeadBlockFmt, enc, v4BlockSize, size, sel, 42)
				c5, _ := buildBenchChunkFmt(b, ChunkFormatV5, UnorderedWithColumnarHeadBlockFmt, enc, newFormatBlockSize, size, sel, 42)

				stages := benchFilterStages(b)
				se, err := log.NewLineSampleExtractor(ex.fn, stages, nil, false, false)
				require.NoError(b, err)
				streamExtractor := se.ForStream(labels.FromStrings("app", "bench"))

				name := fmt.Sprintf("%s/size=%s/selectivity=%.2f", ex.name, humanize.Bytes(size), sel)

				b.Run(name+"/V4-LineByLine", func(b *testing.B) {
					_, ctx := stats.NewContext(context.Background())
					b.ReportAllocs()
					b.SetBytes(int64(totalBytes))
					for range b.N {
						drainOldSamples(ctx, c4, streamExtractor)
					}
				})

				b.Run(name+"/V5-Batch", func(b *testing.B) {
					_, ctx := stats.NewContext(context.Background())
					b.ReportAllocs()
					b.SetBytes(int64(totalBytes))
					for range b.N {
						drainNewSamples(ctx, c5, streamExtractor)
					}
				})
			}
		}
	}
}
