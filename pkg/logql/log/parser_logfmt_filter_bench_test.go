package log

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
)

// BenchmarkLogfmtLabelFilter compares `| logfmt | level="error"` row by row (Process)
// with the columnar path (ProcessBatch), up to the LabelsResult of the entries that pass.
//
// The matrix is value length x pairs per line x share of lines with level=error x label sets.
//
//   - errors is the percentage of lines that pass the filter. The row path stops at the filter for the
//     others, so it does not build their LabelsResult either. That is what the columnar path
//     competes against: it parses everything, and builds labels only for the survivors.
//   - sets=unique gives every line other values. sets=repeated repeats 100 lines, so the label sets
//     repeat. The row path caches its LabelsResult by the hash of the labels, which only hits for
//     repeated sets. The columnar path has no such cache yet.
//
// Implementations:
//
//   - Process calls StreamPipeline.Process for every line and reads the LabelsResult of the
//     lines that pass. The result is complete: the row path computes the string and the hash eagerly.
//   - ProcessBatch calls StreamPipeline.ProcessBatch once. It parses and filters, and ends with the
//     columns and the Selection. It builds no labels, it is the reference for the parse.
//   - ProcessBatch+LabelsResult also builds ArrowBatch.LabelsResult(row) for every selected row and
//     reads the same things as Process: the string, the hash, the parsed and the structured metadata.
//   - ProcessBatch+LabelsResult(String) only reads the string. It is what the iterators group by.
//
// ns/entry is per line of the input, not per line that passes. Building the inputs is not timed.
func BenchmarkLogfmtLabelFilter(b *testing.B) {
	const entries = 10_000
	for _, lengths := range []string{"inline", "long"} {
		for _, numPairs := range []int{5, 20} {
			for _, errorPct := range []int{10, 50, 100} {
				for _, sets := range []string{"unique", "repeated"} {
					name := fmt.Sprintf("strlen=%s/pairs=%d/errors=%d%%/sets=%s", lengths, numPairs, errorPct, sets)
					b.Run(name, func(b *testing.B) {
						lines := generateLevelLines(entries, numPairs, lengths == "long", errorPct, sets == "repeated")
						benchmarkLogfmtLabelFilter(b, lines)
					})
				}
			}
		}
	}
}

// labelsSink keeps the benchmarks from being optimized away.
var labelsSink int

// consumeLabels reads what the entry iterator reads of a result, and its hash.
func consumeLabels(res LabelsResult) {
	labelsSink += len(res.String()) + int(res.Hash()&1) + res.Parsed().Len() + res.StructuredMetadata().Len()
}

func benchmarkLogfmtLabelFilter(b *testing.B, lines []string) {
	entries := len(lines)

	filter := NewStringLabelFilter(labels.MustNewMatcher(labels.MatchEqual, "level", "error"))
	require.IsType(b, &LineFilterLabelFilter{}, filter)
	newPipeline := func() StreamPipeline {
		return NewPipeline([]Stage{NewLogfmtParser(false, false), filter}).ForStream(testStreamLabels)
	}
	pipeline := newPipeline()

	batch := newLogfmtBatch(lines)
	batch.BaseLabels = pipeline.BaseLabels()
	selection := make([]int, entries)
	// The filter compacts the selection in place, so it is set again for every call.
	resetSelection := func() {
		for i := range selection {
			selection[i] = i
		}
		batch.Selection = selection
	}

	// Verify that both paths agree before timing anything: the same rows pass, and a sample of
	// them has the same labels.
	var want []int
	wantResults := map[int]LabelsResult{}
	for row, line := range lines {
		_, res, ok := pipeline.Process(0, unsafeGetBytes(line), labels.EmptyLabels())
		if !ok {
			continue
		}
		want = append(want, row)
		if len(want)%97 == 0 {
			wantResults[row] = res
		}
	}
	require.NotEmpty(b, want)
	resetSelection()
	got := pipeline.ProcessBatch(batch)
	require.Equal(b, want, got.Selection)
	for row, res := range wantResults {
		gotRes := got.LabelsResult(row)
		require.Equal(b, res.String(), gotRes.String(), "line %q", lines[row])
		require.Equal(b, res.Hash(), gotRes.Hash(), "line %q", lines[row])
		require.Equal(b, res.Parsed().String(), gotRes.Parsed().String(), "line %q", lines[row])
		require.Equal(b, res.Stream().String(), gotRes.Stream().String(), "line %q", lines[row])
		require.Equal(b, res.StructuredMetadata().String(), gotRes.StructuredMetadata().String(), "line %q", lines[row])
		require.Equal(b, res.Labels().String(), gotRes.Labels().String(), "line %q", lines[row])
	}

	// The cache of the batch holds one result per distinct label set of the rows that pass, up
	// to its limit: 100 for sets=repeated at 100%, and the limit for most of sets=unique.
	distinct := map[string]struct{}{}
	for _, row := range want {
		distinct[lines[row]] = struct{}{}
	}
	for _, row := range got.Selection {
		got.LabelsResult(row)
	}
	require.Equal(b, min(len(distinct), defaultMaxCachedLabelSets), got.LabelsCache.Len(), "results in the cache")
	got.ParsedLabelsView.Release()

	var bytesPerOp int64
	for _, l := range lines {
		bytesPerOp += int64(len(l))
	}

	b.Run("impl=Process", func(b *testing.B) {
		b.SetBytes(bytesPerOp)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// The pipeline caches its results by the hash of the labels. One that is reused would
			// have seen every line after the first iteration, so every label set would hit the cache,
			// including those of sets=unique. A new pipeline starts with an empty cache, like a
			// stream does: a label set is only cached after it was seen once.
			pipeline := newPipeline()
			for _, line := range lines {
				if _, res, ok := pipeline.Process(0, unsafeGetBytes(line), labels.EmptyLabels()); ok {
					consumeLabels(res)
				}
			}
		}
		reportPerEntry(b, entries)
	})

	b.Run("impl=ProcessBatch", func(b *testing.B) {
		b.SetBytes(bytesPerOp)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			resetSelection()
			out := pipeline.ProcessBatch(batch)
			labelsSink += len(out.Selection)
			out.ParsedLabelsView.Release()
		}
		reportPerEntry(b, entries)
	})

	b.Run("impl=ProcessBatch+LabelsResult", func(b *testing.B) {
		b.SetBytes(bytesPerOp)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			resetSelection()
			out := pipeline.ProcessBatch(batch)
			for _, row := range out.Selection {
				consumeLabels(out.LabelsResult(row))
			}
			out.ParsedLabelsView.Release()
		}
		reportPerEntry(b, entries)
	})

	b.Run("impl=ProcessBatch+LabelsResult(String)", func(b *testing.B) {
		b.SetBytes(bytesPerOp)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			resetSelection()
			out := pipeline.ProcessBatch(batch)
			for _, row := range out.Selection {
				labelsSink += len(out.LabelsResult(row).String())
			}
			out.ParsedLabelsView.Release()
		}
		reportPerEntry(b, entries)
	})
}

// generateLevelLines returns lines of numPairs pairs each, with the key level in the middle.
// The level of a line is error if i%100 < errorPct, and info otherwise. If repeated is true there
// are only 100 different lines, so the label sets repeat. Otherwise the values are all different.
func generateLevelLines(entries, numPairs int, long bool, errorPct int, repeated bool) []string {
	rng := rand.New(rand.NewSource(1))

	value := func() string {
		if long {
			return fmt.Sprintf("a_long_value_that_is_not_inline_%d", rng.Intn(1_000_000))
		}
		return fmt.Sprintf("val%d", rng.Intn(1_000_000))
	}
	line := func(i int) string {
		var sb strings.Builder
		for p := 0; p < numPairs; p++ {
			if p > 0 {
				sb.WriteByte(' ')
			}
			if p == numPairs/2 {
				level := "info"
				if i%100 < errorPct {
					level = "error"
				}
				sb.WriteString("level=" + level)
				continue
			}
			sb.WriteString(fmt.Sprintf("key_%d=%s", p, value()))
		}
		return sb.String()
	}

	lines := make([]string, entries)
	if repeated {
		templates := make([]string, 100)
		for i := range templates {
			templates[i] = line(i)
		}
		for i := range lines {
			lines[i] = templates[i%100]
		}
		return lines
	}
	for i := range lines {
		lines[i] = line(i)
	}
	return lines
}
