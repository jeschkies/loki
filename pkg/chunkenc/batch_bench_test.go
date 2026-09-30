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

// benchNeedle is the substring a chain of two `|= benchNeedle` filters looks
// for, matching the pkg/logql/log ProcessBatch benchmarks' pattern.
const benchNeedle = "matchme"

// buildBenchChunk builds a single real MemChunk (compressed, cut into
// multiple blocks once it exceeds blockSize) containing lines until
// totalBytes worth have been appended, with selectivity the approximate
// fraction of lines containing benchNeedle. Unlike the pkg/logql/log
// ProcessBatch benchmarks (which build an ArrowBatch directly from a
// []string), this exercises the real compressed on-disk block format end to
// end, including decompression.
func buildBenchChunk(tb testing.TB, enc compression.Codec, blockSize int, totalBytes uint64, selectivity float64, seed int64) (*MemChunk, uint64) {
	tb.Helper()
	r := rand.New(rand.NewSource(seed))
	// targetSize is set far above totalBytes so the chunk is never cut
	// early - we want one chunk split into multiple *blocks* by blockSize.
	c := NewMemChunk(ChunkFormatV4, enc, UnorderedWithStructuredMetadataHeadBlockFmt, blockSize, 1<<30)

	var size uint64
	i := int64(0)
	for size < totalBytes {
		field := ""
		if r.Float64() < selectivity {
			field = benchNeedle
		}
		line := fmt.Sprintf("logline=%d level=info method=GET path=/api/v1/query duration=%dms extra=%s",
			i, r.Intn(1000), field)
		entry := &logproto.Entry{Timestamp: time.Unix(0, i), Line: line}
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

// benchFilterStages builds two real, production `|= benchNeedle` filter
// stages via log.NewFilter, the same constructor the query engine uses for
// a line filter expression - not a test-only stand-in.
func benchFilterStages(tb testing.TB) []log.Stage {
	tb.Helper()
	f1, err := log.NewFilter(benchNeedle, log.LineMatchEqual)
	require.NoError(tb, err)
	f2, err := log.NewFilter(benchNeedle, log.LineMatchEqual)
	require.NoError(tb, err)
	return []log.Stage{f1.ToStage(), f2.ToStage()}
}

// drainOldEntries and drainNewEntries bypass encBlock.Iterator's dispatch
// (which now always picks the batch path) and call the old and new
// per-block iterator constructors directly on the same compressed block
// bytes, for a true apples-to-apples comparison.
func drainOldEntries(ctx context.Context, c *MemChunk, pipeline log.StreamPipeline) int {
	n := 0
	for _, b := range c.blocks {
		it := newEntryIterator(ctx, compression.GetReaderPool(c.encoding), b.b, pipeline, c.format, c.symbolizer)
		for it.Next() {
			n++
		}
		_ = it.Close()
	}
	return n
}

func drainNewEntries(ctx context.Context, c *MemChunk, pipeline log.StreamPipeline) int {
	n := 0
	for _, b := range c.blocks {
		it := newBatchEntryIterator(ctx, compression.GetReaderPool(c.encoding), b.b, pipeline, c.format, c.symbolizer, b.numEntries, b.uncompressedSize)
		for it.Next() {
			n++
		}
		_ = it.Close()
	}
	return n
}

func drainOldSamples(ctx context.Context, c *MemChunk, extractor log.StreamSampleExtractor) int {
	n := 0
	for _, b := range c.blocks {
		it := newSampleIterator(ctx, compression.GetReaderPool(c.encoding), b.b, c.format, c.symbolizer, extractor)
		for it.Next() {
			n++
		}
		_ = it.Close()
	}
	return n
}

func drainNewSamples(ctx context.Context, c *MemChunk, extractor log.StreamSampleExtractor) int {
	n := 0
	for _, b := range c.blocks {
		it := newBatchSampleIterator(ctx, compression.GetReaderPool(c.encoding), b.b, c.format, c.symbolizer, extractor, b.numEntries, b.uncompressedSize)
		for it.Next() {
			n++
		}
		_ = it.Close()
	}
	return n
}

// BenchmarkChunkFilter_OldVsBatch compares the old per-line
// decode-then-Process iterator against the new decode-block-then-ProcessBatch
// iterator, for a chain of two `|= "matchme"` filters, on a real compressed
// MemChunk (LZ4, chunkenc's default block size).
func BenchmarkChunkFilter_OldVsBatch(b *testing.B) {
	sizes := []uint64{256 * humanize.KiByte, 1 * humanize.MiByte, 4 * humanize.MiByte}
	selectivities := []float64{1.0, 0.1, 0.0}
	const enc = compression.LZ4_256k

	for _, size := range sizes {
		for _, sel := range selectivities {
			c, totalBytes := buildBenchChunk(b, enc, 256*1024, size, sel, 42)
			stages := benchFilterStages(b)
			pipeline := log.NewPipeline(stages).ForStream(labels.FromStrings("app", "bench"))

			name := fmt.Sprintf("size=%s/selectivity=%.2f", humanize.Bytes(size), sel)

			b.Run(name+"/LineByLine", func(b *testing.B) {
				_, ctx := stats.NewContext(context.Background())
				b.ReportAllocs()
				b.SetBytes(int64(totalBytes))
				for range b.N {
					drainOldEntries(ctx, c, pipeline)
				}
			})

			b.Run(name+"/Batch", func(b *testing.B) {
				_, ctx := stats.NewContext(context.Background())
				b.ReportAllocs()
				b.SetBytes(int64(totalBytes))
				for range b.N {
					drainNewEntries(ctx, c, pipeline)
				}
			})
		}
	}
}

// BenchmarkChunkSample_OldVsBatch is BenchmarkChunkFilter_OldVsBatch's
// StreamSampleExtractor counterpart, for count_over_time and
// bytes_over_time behind the same two-filter chain.
func BenchmarkChunkSample_OldVsBatch(b *testing.B) {
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
				c, totalBytes := buildBenchChunk(b, enc, 256*1024, size, sel, 42)
				stages := benchFilterStages(b)
				se, err := log.NewLineSampleExtractor(ex.fn, stages, nil, false, false)
				require.NoError(b, err)
				streamExtractor := se.ForStream(labels.FromStrings("app", "bench"))

				name := fmt.Sprintf("%s/size=%s/selectivity=%.2f", ex.name, humanize.Bytes(size), sel)

				b.Run(name+"/LineByLine", func(b *testing.B) {
					_, ctx := stats.NewContext(context.Background())
					b.ReportAllocs()
					b.SetBytes(int64(totalBytes))
					for range b.N {
						drainOldSamples(ctx, c, streamExtractor)
					}
				})

				b.Run(name+"/Batch", func(b *testing.B) {
					_, ctx := stats.NewContext(context.Background())
					b.ReportAllocs()
					b.SetBytes(int64(totalBytes))
					for range b.N {
						drainNewSamples(ctx, c, streamExtractor)
					}
				})
			}
		}
	}
}
