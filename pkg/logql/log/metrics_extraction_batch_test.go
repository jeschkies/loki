package log

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/dustin/go-humanize"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
)

func newLineSampleExtractorStream(t testing.TB, ex LineExtractor, stages []Stage) StreamSampleExtractor {
	t.Helper()
	se, err := NewLineSampleExtractor(ex, stages, nil, false, false)
	require.NoError(t, err)
	return se.ForStream(labels.FromStrings("app", "test"))
}

// TestStreamLineSampleExtractor_ProcessBatch_MatchesProcess checks that
// ProcessBatch (which uses CountExtractorBatch/BytesExtractorBatch's
// vectorized fast path once containsFilter stages have narrowed the
// selection) produces the same values as calling Process per line, for
// both count_over_time and bytes_over_time's extractors.
func TestStreamLineSampleExtractor_ProcessBatch_MatchesProcess(t *testing.T) {
	lines := []string{
		"foo bar baz",
		"has needle1",
		"nothing here",
		"has needle1 and needle2",
		"only needle2",
		"has needle1 needle2 and needle3",
		"empty",
	}

	for _, ex := range []struct {
		name string
		fn   LineExtractor
	}{
		{"count_over_time", CountExtractor},
		{"bytes_over_time", BytesExtractor},
	} {
		t.Run(ex.name, func(t *testing.T) {
			stages := []Stage{containsStage(t, "needle1"), containsStage(t, "needle2")}
			se := newLineSampleExtractorStream(t, ex.fn, stages)

			batch := buildArrowBatch(t, lines)
			resultBatch, values := se.ProcessBatch(batch)

			var wantRows []int32
			var wantValues []float64
			for i, l := range lines {
				sample, ok := se.Process(0, []byte(l), labels.EmptyLabels())
				if !ok {
					continue
				}
				wantRows = append(wantRows, int32(i))
				wantValues = append(wantValues, sample.Value)
			}

			require.Equal(t, wantRows, resultBatch.Selection)
			require.Equal(t, wantValues, values)
		})
	}
}

// TestStreamLineSampleExtractor_ProcessBatch_DownshiftsForParser verifies
// that a chain mixing a batch-capable filter with a non-batch-capable
// stage (a real LogfmtParser) still produces correct values end-to-end,
// exercising runRemainingStagesAndExtractPerRow.
func TestStreamLineSampleExtractor_ProcessBatch_DownshiftsForParser(t *testing.T) {
	lines := []string{
		`level=info msg="has needle"`,
		`level=error msg="also has needle"`,
		`level=info msg="no match here"`,
	}

	stages := []Stage{containsStage(t, "needle"), NewLogfmtParser(false, false)}
	se := newLineSampleExtractorStream(t, BytesExtractor, stages)

	batch := buildArrowBatch(t, lines)
	resultBatch, values := se.ProcessBatch(batch)

	require.Equal(t, []int32{0, 1}, resultBatch.Selection)
	require.Equal(t, []float64{float64(len(lines[0])), float64(len(lines[1]))}, values)

	// The logfmt parser should still have added a "level" label per
	// surviving row, exactly as it would running line-by-line.
	require.Len(t, resultBatch.Labels, 2)
	for _, lr := range resultBatch.Labels {
		require.True(t, lr.Labels().Has("level"))
	}
}

// buildSampleBenchLines mirrors buildFilterBenchLines from batch_test.go,
// generating lines until their total size reaches size bytes, with
// selectivity the approximate fraction containing needle.
func buildSampleBenchLines(size uint64, selectivity float64, seed int64) (lines []string, totalBytes uint64) {
	r := rand.New(rand.NewSource(seed))
	for i := 0; totalBytes < size; i++ {
		field := ""
		if r.Float64() < selectivity {
			field = "matchme"
		}
		line := fmt.Sprintf("logline=%d level=info method=GET path=/api/v1/query duration=%dms extra=%s",
			i, r.Intn(1000), field)
		lines = append(lines, line)
		totalBytes += uint64(len(line))
	}
	return lines, totalBytes
}

// BenchmarkStreamSampleExtractor_BatchVsLineByLine compares ProcessBatch
// against calling Process once per line (simulating today's
// sampleBufferedIterator.Next() in pkg/chunkenc), for a chain of 2 `|=
// "matchme"` filters feeding count_over_time and bytes_over_time.
func BenchmarkStreamSampleExtractor_BatchVsLineByLine(b *testing.B) {
	sizes := []uint64{512 * humanize.KiByte, 1 * humanize.MiByte, 4 * humanize.MiByte}
	selectivities := []float64{1.0, 0.3, 0.1, 0.01, 0.0}

	for _, ex := range []struct {
		name string
		fn   LineExtractor
	}{
		{"count_over_time", CountExtractor},
		{"bytes_over_time", BytesExtractor},
	} {
		for _, size := range sizes {
			for _, sel := range selectivities {
				lines, totalBytes := buildSampleBenchLines(size, sel, 42)
				batchTemplate := buildArrowBatch(b, lines)

				stages := []Stage{containsStage(b, "matchme"), containsStage(b, "matchme")}
				se := newLineSampleExtractorStream(b, ex.fn, stages)

				simdStages := []Stage{containsSIMDStage(b, "matchme"), containsSIMDStage(b, "matchme")}
				seSIMD := newLineSampleExtractorStream(b, ex.fn, simdStages)

				name := fmt.Sprintf("%s/size=%s/selectivity=%.2f", ex.name, humanize.Bytes(size), sel)

				b.Run(name+"/LineByLine", func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(totalBytes))
					for range b.N {
						matches := 0
						for _, l := range lines {
							if _, ok := se.Process(0, []byte(l), labels.EmptyLabels()); ok {
								matches++
							}
						}
					}
				})

				b.Run(name+"/Batch", func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(totalBytes))
					for range b.N {
						batch := &ArrowBatch{Timestamps: batchTemplate.Timestamps, LineColumn: batchTemplate.LineColumn}
						_, _ = se.ProcessBatch(batch)
					}
				})

				b.Run(name+"/BatchSIMD", func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(totalBytes))
					for range b.N {
						batch := &ArrowBatch{Timestamps: batchTemplate.Timestamps, LineColumn: batchTemplate.LineColumn}
						_, _ = seSIMD.ProcessBatch(batch)
					}
				})
			}
		}
	}
}

// TestStreamLineSampleExtractor_ProcessBatch_SIMDMatchesStdlib verifies the
// SIMD-backed stage variant used for benchmarking produces identical
// results to the production stdlib-backed one.
func TestStreamLineSampleExtractor_ProcessBatch_SIMDMatchesStdlib(t *testing.T) {
	lines := []string{
		"foo bar baz",
		"has needle1",
		"nothing here",
		"has needle1 and needle2",
		"only needle2",
		"has needle1 needle2 and needle3",
		"empty",
	}

	stdlib := newLineSampleExtractorStream(t, BytesExtractor, []Stage{containsStage(t, "needle1"), containsStage(t, "needle2")})
	simd := newLineSampleExtractorStream(t, BytesExtractor, []Stage{containsSIMDStage(t, "needle1"), containsSIMDStage(t, "needle2")})

	batch1 := buildArrowBatch(t, lines)
	batch2 := buildArrowBatch(t, lines)

	resultStdlib, valuesStdlib := stdlib.ProcessBatch(batch1)
	resultSIMD, valuesSIMD := simd.ProcessBatch(batch2)

	require.Equal(t, resultStdlib.Selection, resultSIMD.Selection)
	require.Equal(t, valuesStdlib, valuesSIMD)
}
