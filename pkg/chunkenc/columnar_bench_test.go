package chunkenc

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/v3/pkg/compression"
	"github.com/grafana/loki/v3/pkg/logproto"
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

// buildBenchChunkWithStructuredMetadata builds a V5 chunk with
// structured metadata (2 key/value pairs) on every line - the worst
// case for ArrowBatch's lazy structured-metadata resolution (see
// decodeColumnarBlockToArrowBatch/ArrowBatch.get): with no line
// filter ahead of it and no selectivity to speak of, every row
// reaches get() and pays the resolution cost, same as the old eager
// []labels.Labels approach paid at decode time for every row
// regardless.
func buildBenchChunkWithStructuredMetadata(tb testing.TB, blockSize int, totalBytes uint64, seed int64) (*MemChunk, uint64) {
	tb.Helper()
	r := rand.New(rand.NewSource(seed))
	c := NewMemChunk(ChunkFormatV5, compression.LZ4_256k, UnorderedWithColumnarHeadBlockFmt, blockSize, 1<<30)

	var size uint64
	i := int64(0)
	for size < totalBytes {
		line := fmt.Sprintf("logline=%d level=info duration=%dms", i, r.Intn(1000))
		meta := []logproto.LabelAdapter{
			{Name: "trace_id", Value: fmt.Sprintf("t%d", i)},
			{Name: "pod", Value: "pod-a"},
		}
		entry := &logproto.Entry{Timestamp: time.Unix(0, i), Line: line, StructuredMetadata: meta}
		if !c.SpaceFor(entry) {
			break
		}
		_, err := c.Append(entry)
		require.NoError(tb, err)
		size += uint64(len(line))
		i++
	}
	require.NoError(tb, c.Close())
	return c, size
}

// BenchmarkDecodeStructuredMetadata measures decode + full per-row
// structured-metadata resolution cost (via a no-op pipeline, so every
// row survives to materializeBatchLabels/get() - no batch-capable
// stage drops anything first).
func BenchmarkDecodeStructuredMetadata(b *testing.B) {
	sizes := []uint64{1 * humanize.MiByte, 4 * humanize.MiByte}
	pipeline := log.NewPipeline(nil).ForStream(labels.FromStrings("app", "bench"))

	for _, size := range sizes {
		c, totalBytes := buildBenchChunkWithStructuredMetadata(b, newFormatBlockSize, size, 42)
		name := fmt.Sprintf("size=%s", humanize.Bytes(size))

		b.Run(name, func(b *testing.B) {
			_, ctx := stats.NewContext(context.Background())
			b.ReportAllocs()
			b.SetBytes(int64(totalBytes))
			for range b.N {
				drainNewEntries(ctx, c, pipeline)
			}
		})
	}
}
