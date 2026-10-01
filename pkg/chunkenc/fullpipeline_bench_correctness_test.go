package chunkenc

import (
	"context"
	"testing"

	"github.com/dustin/go-humanize"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/v3/pkg/arrowfilter"
	"github.com/grafana/loki/v3/pkg/compression"
	"github.com/grafana/loki/v3/pkg/logql/log"
	"github.com/grafana/loki/v3/pkg/logqlmodel/stats"
)

// TestFullPipeline_AllVariantsAgree checks that RowAtATime-Full,
// ColumnarSIMD-ProductionPath, StringView-Full, and PackedStringView-SIMD-Full
// all count the exact same number of matches for
// `|= benchNeedle | logfmt | level=<matchValue> | count_over_time`, across
// a few selectivities and both an inline and a non-inline level value -
// before trusting BenchmarkFullPipeline_DecodeFilterLogfmtCount's timings,
// since the hand-assembled variants' block-level logic (and now the AVX2
// tier-1 scan) are new and easy to get subtly wrong (off-by-one in the
// intersection, wrong selection semantics, etc).
func TestFullPipeline_AllVariantsAgree(t *testing.T) {
	const enc = compression.LZ4_256k
	size := uint64(1 * humanize.MiByte)

	for _, matchValue := range []string{"info", "a-very-long-level-value-not-inlined-here"} {
		lineFilter, err := log.NewFilter(benchNeedle, log.LineMatchEqual)
		require.NoError(t, err)
		levelMatcher := labels.MustNewMatcher(labels.MatchEqual, "level", matchValue)
		wantEscaped := []byte(matchValue)

		newExtractor := func() log.StreamSampleExtractor {
			stages := []log.Stage{lineFilter.ToStage(), log.NewLogfmtParser(false, false), log.NewStringLabelFilter(levelMatcher)}
			se, err := log.NewLineSampleExtractor(log.CountExtractor, stages, nil, false, false)
			require.NoError(t, err)
			return se.ForStream(labels.FromStrings("app", "bench"))
		}

		for _, sel := range []float64{1.0, 0.5, 0.1, 0.0} {
			c4, _ := buildBenchChunkFmtLogfmtCount(t, ChunkFormatV4, UnorderedWithStructuredMetadataHeadBlockFmt, enc, v4BlockSize, size, sel, matchValue, 7)
			c5, _ := buildBenchChunkFmtLogfmtCount(t, ChunkFormatV5, UnorderedWithColumnarHeadBlockFmt, enc, newFormatBlockSize, size, sel, matchValue, 7)

			_, ctx := stats.NewContext(context.Background())
			wantRowAtATime := drainOldSamples(ctx, c4, newExtractor())
			wantColumnar := drainNewSamples(ctx, c5, newExtractor())

			pool := compression.GetReaderPool(c5.encoding)
			gotStringView := 0
			gotPacked := 0
			for _, blk := range c5.blocks {
				block, err := decodeBlockBytesToArrowBatch(ctx, pool, blk.b, c5.format, c5.symbolizer, blk.numEntries, blk.uncompressedSize)
				require.NoError(t, err)

				lineSel := arrowfilter.Contains(block.LineColumn, []byte(benchNeedle), nil)
				if len(lineSel) > 0 {
					view := log.BuildLogfmtValueView(arrowAllocator, block.LineColumn, []byte("level"))
					valSel := log.FilterLogfmtValueView(view, wantEscaped)
					gotStringView += intersectCount(lineSel, valSel)
					arrowfilter.PutSelection(valSel)
					view.Release()

					// BuildPackedStringView takes lineSel directly, so
					// pvalSel is already an index into lineSel - every
					// match is a final result, no intersection needed
					// (contrast with the StringView-Full/gotStringView
					// path just above, which still scans every row).
					pview := log.BuildPackedStringView(arrowAllocator, block.LineColumn, []byte("level"), lineSel)
					pvalSel := log.FilterPackedStringViewSIMD(pview, wantEscaped)
					gotPacked += len(pvalSel)
					arrowfilter.PutSelection(pvalSel)
					pview.Release()
				}
				arrowfilter.PutSelection(lineSel)
				block.Release()
			}

			require.Equal(t, wantRowAtATime, wantColumnar, "matchValue %q selectivity %.2f: RowAtATime vs ColumnarSIMD production path disagree", matchValue, sel)
			require.Equal(t, wantRowAtATime, gotStringView, "matchValue %q selectivity %.2f: RowAtATime vs StringView-Full disagree", matchValue, sel)
			require.Equal(t, wantRowAtATime, gotPacked, "matchValue %q selectivity %.2f: RowAtATime vs PackedStringView-SIMD-Full disagree", matchValue, sel)
		}
	}
}
