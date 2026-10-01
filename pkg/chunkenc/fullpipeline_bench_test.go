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

	"github.com/grafana/loki/v3/pkg/arrowfilter"
	"github.com/grafana/loki/v3/pkg/compression"
	"github.com/grafana/loki/v3/pkg/logproto"
	"github.com/grafana/loki/v3/pkg/logql/log"
	"github.com/grafana/loki/v3/pkg/logqlmodel/stats"
)

// buildBenchChunkFmtLogfmtCount builds a chunk of logfmt-shaped lines for
// the full-pipeline benchmark: `{app="bench"} |= benchNeedle | logfmt |
// level=<matchValue> | count_over_time`. A single selectivity couples
// both predicates together (a row either matches both the line filter
// and the label filter, or neither) - a simplification of the real,
// independent selectivities a production query would have, called out
// explicitly rather than silently assumed.
//
// matchValue is the level value matching rows get (non-matching rows
// get "warn" instead) - pass a value <= 12 bytes to exercise the
// PackedStringView inline path, or > 12 bytes for the out-of-line,
// buffer-referenced path.
func buildBenchChunkFmtLogfmtCount(tb testing.TB, chunkFmt byte, headFmt HeadBlockFmt, enc compression.Codec, blockSize int, totalBytes uint64, sel float64, matchValue string, seed int64) (*MemChunk, uint64) {
	tb.Helper()
	r := rand.New(rand.NewSource(seed))
	c := NewMemChunk(chunkFmt, enc, headFmt, blockSize, 1<<30)

	var size uint64
	i := int64(0)
	for size < totalBytes {
		level, extra := "warn", ""
		if r.Float64() < sel {
			level, extra = matchValue, benchNeedle
		}
		line := fmt.Sprintf("logline=%d level=%s method=GET path=/api/v1/query duration=%dms extra=%s",
			i, level, r.Intn(1000), extra)
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

// intersectCount counts positions present in both a and b - two ascending
// int32 lists over the same row-index space - via a merge-join, without
// materializing the intersection itself.
func intersectCount(a, b []int32) int {
	i, j, n := 0, 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			i++
		case a[i] > b[j]:
			j++
		default:
			n++
			i++
			j++
		}
	}
	return n
}

// runFullPipelineBenchmark is BenchmarkFullPipeline_DecodeFilterLogfmtCount's
// shared body, parameterized by matchValue so the same four variants can
// be measured against both an inline (<=12 byte) and a non-inline
// (>12 byte) level value - see buildBenchChunkFmtLogfmtCount.
//
//   - RowAtATime-Full: the pre-existing baseline end to end - V4 row-major
//     decode, Process called per line for the filter/parser/label-filter
//     chain, via the real log.NewLineSampleExtractor/drainOldSamples.
//   - ColumnarSIMD-ProductionPath: what's *actually wired into production
//     today* - V5 columnar decode + ProcessBatch, whose SIMD line filter
//     runs in batch mode and then falls back to Process per row for
//     LogfmtParser/StringLabelFilter (neither implements BatchProcessor).
//     Via drainNewSamples, no new glue code.
//   - StringView-Full: every exploratory piece from this session assembled
//     by hand (not wired into StreamSampleExtractor) - V5 columnar decode,
//     arrowfilter.Contains for the line filter, BuildLogfmtValueView +
//     FilterLogfmtValueView for the logfmt label filter, intersected with
//     the line filter's selection, then counted for count_over_time.
//   - PackedStringView-SIMD-Full: BuildPackedStringView + FilterPackedStringViewSIMD
//     (AVX2 tier-1 scan via matchLo, see pkg/logql/log/matchlo_amd64.s) in
//     place of the array.StringView-based logfmt column - and, unlike
//     StringView-Full, BuildPackedStringView takes the line filter's
//     selection directly: findLogfmtValueSpan isn't SIMD, so there's no
//     reason to pay for it on rows the line filter already dropped. Its
//     output is therefore already final (an index into the selection,
//     not the block), with no intersection step needed afterward.
func runFullPipelineBenchmark(b *testing.B, sizes []uint64, selectivities []float64, matchValue string) {
	const enc = compression.LZ4_256k

	lineFilter, err := log.NewFilter(benchNeedle, log.LineMatchEqual)
	require.NoError(b, err)
	levelMatcher := labels.MustNewMatcher(labels.MatchEqual, "level", matchValue)
	wantEscaped := []byte(matchValue) // no logfmt quoting needed for these literals

	for _, size := range sizes {
		for _, sel := range selectivities {
			c4, totalBytes := buildBenchChunkFmtLogfmtCount(b, ChunkFormatV4, UnorderedWithStructuredMetadataHeadBlockFmt, enc, v4BlockSize, size, sel, matchValue, 42)
			c5, _ := buildBenchChunkFmtLogfmtCount(b, ChunkFormatV5, UnorderedWithColumnarHeadBlockFmt, enc, newFormatBlockSize, size, sel, matchValue, 42)

			name := fmt.Sprintf("size=%s/selectivity=%.2f", humanize.Bytes(size), sel)

			newExtractor := func(b *testing.B) log.StreamSampleExtractor {
				stages := []log.Stage{lineFilter.ToStage(), log.NewLogfmtParser(false, false), log.NewStringLabelFilter(levelMatcher)}
				se, err := log.NewLineSampleExtractor(log.CountExtractor, stages, nil, false, false)
				require.NoError(b, err)
				return se.ForStream(labels.FromStrings("app", "bench"))
			}

			b.Run(name+"/RowAtATime-Full", func(b *testing.B) {
				pipeline := newExtractor(b)
				_, ctx := stats.NewContext(context.Background())
				b.ReportAllocs()
				b.SetBytes(int64(totalBytes))
				for range b.N {
					drainOldSamples(ctx, c4, pipeline)
				}
			})

			b.Run(name+"/ColumnarSIMD-ProductionPath", func(b *testing.B) {
				pipeline := newExtractor(b)
				_, ctx := stats.NewContext(context.Background())
				b.ReportAllocs()
				b.SetBytes(int64(totalBytes))
				for range b.N {
					drainNewSamples(ctx, c5, pipeline)
				}
			})

			b.Run(name+"/StringView-Full", func(b *testing.B) {
				pool := compression.GetReaderPool(c5.encoding)
				base := labels.FromStrings("app", "bench")
				lbs := log.NewBaseLabelsBuilder().ForLabels(base, labels.StableHash(base))
				_, ctx := stats.NewContext(context.Background())
				b.ReportAllocs()
				b.SetBytes(int64(totalBytes))
				for range b.N {
					total := 0
					for _, blk := range c5.blocks {
						block, err := decodeBlockBytesToArrowBatch(ctx, pool, blk.b, c5.format, c5.symbolizer, blk.numEntries, blk.uncompressedSize)
						require.NoError(b, err)

						lineSel := arrowfilter.Contains(block.LineColumn, []byte(benchNeedle), nil)
						// Mirror runBatchCapableStages' own short-circuit
						// (pkg/logql/log/batch.go): skip the logfmt scan
						// entirely once the line filter has already
						// emptied the selection, instead of always paying
						// for it regardless of outcome.
						if len(lineSel) > 0 {
							view := log.BuildLogfmtValueView(arrowAllocator, block.LineColumn, []byte("level"))
							valSel := log.FilterLogfmtValueView(view, wantEscaped)

							// Materialize LabelsResult for every matched row,
							// same as sampleBatchBufferedIterator.Next()'s
							// lr.String() - required for the merge
							// iterator's dedup hash, not optional, so a fair
							// comparison has to pay for it here too.
							i, j := 0, 0
							for i < len(lineSel) && j < len(valSel) {
								switch {
								case lineSel[i] < valSel[j]:
									i++
								case lineSel[i] > valSel[j]:
									j++
								default:
									lr := log.MaterializeLogfmtValueForRow(view, "level", int(valSel[j]), lbs)
									_ = lr.String()
									total++
									i++
									j++
								}
							}

							arrowfilter.PutSelection(valSel)
							view.Release()
						}
						arrowfilter.PutSelection(lineSel)
						block.Release()
					}
					_ = total
				}
			})

			b.Run(name+"/PackedStringView-SIMD-Full", func(b *testing.B) {
				pool := compression.GetReaderPool(c5.encoding)
				base := labels.FromStrings("app", "bench")
				lbs := log.NewBaseLabelsBuilder().ForLabels(base, labels.StableHash(base))
				_, ctx := stats.NewContext(context.Background())
				b.ReportAllocs()
				b.SetBytes(int64(totalBytes))
				for range b.N {
					total := 0
					for _, blk := range c5.blocks {
						block, err := decodeBlockBytesToArrowBatch(ctx, pool, blk.b, c5.format, c5.symbolizer, blk.numEntries, blk.uncompressedSize)
						require.NoError(b, err)

						lineSel := arrowfilter.Contains(block.LineColumn, []byte(benchNeedle), nil)
						if len(lineSel) > 0 {
							// BuildPackedStringView only scans rows the line
							// filter already selected - findLogfmtValueSpan
							// isn't SIMD, so there's no reason to pay for it
							// on rows the line filter has already dropped.
							// valSel is therefore an index into lineSel, not
							// into the block directly; every match it finds
							// is already a final result, so no intersection
							// with lineSel is needed afterward (contrast
							// with StringView-Full above, which still scans
							// every row and intersects).
							view := log.BuildPackedStringView(arrowAllocator, block.LineColumn, []byte("level"), lineSel)
							valSel := log.FilterPackedStringViewSIMD(view, wantEscaped)

							for _, idx := range valSel {
								lr := log.MaterializePackedValueForRow(view, "level", int(idx), lbs)
								_ = lr.String()
								total++
							}

							arrowfilter.PutSelection(valSel)
							view.Release()
						}
						arrowfilter.PutSelection(lineSel)
						block.Release()
					}
					_ = total
				}
			})
		}
	}
}

// BenchmarkFullPipeline_DecodeFilterLogfmtCount is the full, realistic
// query shape this whole branch has been building toward: chunk decode +
// SIMD line filter + logfmt label extraction/filter + count_over_time,
// with an inline (4-byte) level value - see runFullPipelineBenchmark.
func BenchmarkFullPipeline_DecodeFilterLogfmtCount(b *testing.B) {
	sizes := []uint64{256 * humanize.KiByte, 1 * humanize.MiByte, 4 * humanize.MiByte}
	selectivities := []float64{1.0, 0.1, 0.0}
	runFullPipelineBenchmark(b, sizes, selectivities, "info")
}

// BenchmarkFullPipeline_DecodeFilterLogfmtCount_LongValue is the same
// benchmark with a 41-byte level value - exercises PackedStringView's
// and array.StringView's out-of-line, buffer-referenced path instead of
// the inline one. Fewer sizes, since this is mainly about confirming the
// non-inline path's relative cost, not re-covering the inline sweep.
func BenchmarkFullPipeline_DecodeFilterLogfmtCount_LongValue(b *testing.B) {
	sizes := []uint64{1 * humanize.MiByte, 4 * humanize.MiByte}
	selectivities := []float64{1.0, 0.1, 0.0}
	runFullPipelineBenchmark(b, sizes, selectivities, "a-very-long-level-value-not-inlined-here")
}
