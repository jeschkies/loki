package log

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/dustin/go-humanize"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/v3/pkg/arrowfilter"
)

// buildArrowBatch builds an ArrowBatch from lines, with timestamps 0..n-1
// and no structured metadata.
func buildArrowBatch(t testing.TB, lines []string) *ArrowBatch {
	t.Helper()

	bldr := array.NewStringBuilder(memory.DefaultAllocator)
	ts := make([]int64, len(lines))
	for i, l := range lines {
		bldr.Append(l)
		ts[i] = int64(i)
	}
	col := bldr.NewStringArray()
	t.Cleanup(col.Release)

	return &ArrowBatch{Timestamps: ts, LineColumn: col}
}

// selectedLines returns the lines corresponding to b's current selection,
// reading either from the materialized b.Lines or directly off LineColumn.
func selectedLines(b *ArrowBatch) []string {
	rows := b.rows()
	out := make([]string, 0, len(rows))
	if b.Lines != nil {
		for _, l := range b.Lines {
			out = append(out, string(l))
		}
		return out
	}
	for _, row := range rows {
		out = append(out, b.LineColumn.Value(int(row)))
	}
	return out
}

func containsStage(t testing.TB, match string) Stage {
	t.Helper()
	f := newContainsFilter([]byte(match), false)
	return f.ToStage()
}

// containsSIMDStage is a benchmark-only variant of containsStage: same
// scalar Filter fallback, but its batch fast path uses
// arrowfilter.ContainsSIMD (go-memmem's AVX2 scan) instead of the
// production containsBatch's arrowfilter.Contains (stdlib bytes.Index),
// to compare the two scans head-to-head through the exact same
// ProcessBatch dispatch. Not wired into containsFilter itself.
func containsSIMDStage(t testing.TB, match string) Stage {
	t.Helper()
	m := []byte(match)
	process := func(_ int64, line []byte, _ *LabelsBuilder) ([]byte, bool) {
		return line, bytes.Contains(line, m)
	}
	return StageFunc{
		process: process,
		processBatch: func(b *ArrowBatch) *ArrowBatch {
			return b.withSelection(arrowfilter.ContainsSIMD(b.LineColumn, m, b.Selection))
		},
	}
}

func TestArrowBatch_MatchesLineByLine(t *testing.T) {
	lines := []string{
		"foo bar baz",
		"has needle1",
		"nothing here",
		"has needle1 and needle2",
		"only needle2",
		"has needle1 needle2 and needle3",
		"empty",
	}

	stages := []Stage{
		containsStage(t, "needle1"),
		containsStage(t, "needle2"),
	}

	pipeline := NewPipeline(stages)
	sp := pipeline.ForStream(labels.FromStrings("app", "test"))

	batch := buildArrowBatch(t, lines)
	got := selectedLines(sp.ProcessBatch(batch))

	var want []string
	for _, l := range lines {
		if _, _, ok := sp.Process(0, []byte(l), labels.EmptyLabels()); ok {
			want = append(want, l)
		}
	}

	require.Equal(t, want, got)
	require.Equal(t, []string{
		"has needle1 and needle2",
		"has needle1 needle2 and needle3",
	}, got)
}

// TestArrowBatch_FallsBackForLabelStage verifies that a chain mixing a
// batch-capable line filter with a label-mutating stage (which has no
// BatchProcessor support) still produces correct results end-to-end.
func TestArrowBatch_FallsBackForLabelStage(t *testing.T) {
	lines := []string{
		`level=info msg="has needle"`,
		`level=error msg="also has needle"`,
		`level=info msg="no match here"`,
	}

	logfmtParser := NewLogfmtParser(false, false)
	stages := []Stage{
		containsStage(t, "needle"),
		logfmtParser,
	}

	pipeline := NewPipeline(stages)
	sp := pipeline.ForStream(labels.FromStrings("app", "test"))

	batch := buildArrowBatch(t, lines)
	result := sp.ProcessBatch(batch)
	got := selectedLines(result)

	require.ElementsMatch(t, []string{
		`level=info msg="has needle"`,
		`level=error msg="also has needle"`,
	}, got)

	// The logfmt parser should have added a "level" label for each
	// surviving row, exactly as it would running line-by-line.
	require.Len(t, result.Labels, 2)
	for _, lr := range result.Labels {
		require.True(t, lr.Labels().Has("level"))
	}
}

const needle = "matchme"

// buildFilterBenchLines generates lines until their total size reaches size
// bytes, where selectivity is the approximate fraction of lines matching
// ALL of the given needles (simulating a chain of `|= needle` filters).
func buildFilterBenchLines(size uint64, selectivity float64, seed int64) (lines []string, totalBytes uint64) {
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

// BenchmarkStreamPipeline_BatchVsLineByLine compares ProcessBatch (which
// uses each containsFilter stage's vectorized fast path) against calling
// Process once per line (simulating today's chunkenc iterator), for a
// chain of 2 and 3 identical `|= "matchme"` filters.
func BenchmarkStreamPipeline_BatchVsLineByLine(b *testing.B) {
	sizes := []uint64{512 * humanize.KiByte, 1 * humanize.MiByte, 4 * humanize.MiByte}
	selectivities := []float64{1.0, 0.3, 0.1, 0.01, 0.0}
	chainLengths := []int{2, 3}

	for _, chainLen := range chainLengths {
		for _, size := range sizes {
			for _, sel := range selectivities {
				lines, totalBytes := buildFilterBenchLines(size, sel, 42)
				batchTemplate := buildArrowBatch(b, lines)

				stages := make([]Stage, chainLen)
				for i := range stages {
					stages[i] = containsStage(b, needle)
				}
				pipeline := NewPipeline(stages)
				sp := pipeline.ForStream(labels.FromStrings("app", "bench"))

				name := fmt.Sprintf("stages=%d/size=%s/selectivity=%.2f", chainLen, humanize.Bytes(size), sel)

				b.Run(name+"/LineByLine", func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(totalBytes))
					for range b.N {
						matches := 0
						for _, l := range lines {
							if _, _, ok := sp.Process(0, []byte(l), labels.EmptyLabels()); ok {
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
						_ = sp.ProcessBatch(batch)
					}
				})
			}
		}
	}
}
