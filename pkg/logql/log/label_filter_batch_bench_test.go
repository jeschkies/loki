package log

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
)

// BenchmarkLabelFilterStructuredMetadata compares filtering structured metadata
// row by row (Process) with the columnar path (ProcessBatch) for the filter
// `| <key>="<value>"`.
//
// The matrix is string length x entries x labels per entry x percentage of
// matching entries.
//
// Strings of up to 12 bytes are stored inline in the view header. Longer strings
// live in the array's data buffers, so the filter has to resolve them through
// the buffer index and offset to verify a match. The "long" variant makes every
// key and value too long to be inline.
//
//   - Process calls StreamPipeline.Process for every row. The pipeline resets its
//     LabelsBuilder, adds the row's structured metadata, runs the stage and
//     builds the LabelsResult of the rows that pass.
//   - ProcessBatch calls StreamPipeline.ProcessBatch once over a
//     Map<StringView, StringView>.
//
// Both sides start from the same data and the same full selection, and both end
// with a selection of the surviving rows. Building the inputs is not timed.
func BenchmarkLabelFilterStructuredMetadata(b *testing.B) {
	for _, lengths := range []struct {
		name       string
		key, value string
	}{
		{"inline", "app", "match"},
		{"long", "application_name_label", "matching_value_that_is_long"},
	} {
		for _, entries := range []int{1_000, 10_000, 100_000} {
			for _, numLabels := range []int{1, 5, 20} {
				for _, matchPct := range []int{0, 10, 50, 100} {
					name := fmt.Sprintf("strlen=%s/entries=%d/labels=%d/matches=%d%%", lengths.name, entries, numLabels, matchPct)
					b.Run(name, func(b *testing.B) {
						benchmarkLabelFilter(b, lengths.key, lengths.value, entries, numLabels, matchPct)
					})
				}
			}
		}
	}
}

func benchmarkLabelFilter(b *testing.B, key, value string, entries, numLabels, matchPct int) {
	rows, lbs := generateStructuredMetadata(entries, numLabels, matchPct, key, value)
	pipeline := NewPipeline([]Stage{NewStringLabelFilter(labels.MustNewMatcher(labels.MatchEqual, key, value))}).ForStream(labels.EmptyLabels())

	full := make([]int, entries)
	for i := range full {
		full[i] = i
	}

	// Verify both paths agree before timing anything.
	want := filterByProcess(pipeline, lbs, full, make([]int, 0, entries))
	check := &ArrowBatch{StructuredMetadataView: newMetadataMap(b, rows), Selection: append([]int(nil), full...)}
	require.Equal(b, want, pipeline.ProcessBatch(check).Selection)

	b.Run("impl=Process", func(b *testing.B) {
		out := make([]int, 0, entries)

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			out = filterByProcess(pipeline, lbs, full, out[:0])
		}
		reportPerEntry(b, entries)
	})

	b.Run("impl=ProcessBatch", func(b *testing.B) {
		batch := &ArrowBatch{StructuredMetadataView: newMetadataMap(b, rows)}
		sel := make([]int, entries)

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// ProcessBatch narrows Selection in place, so restore it.
			copy(sel, full)
			batch.Selection = sel
			_ = pipeline.ProcessBatch(batch)
		}
		reportPerEntry(b, entries)
	})
}

// filterByProcess runs pipeline.Process for every selected row and appends the
// indices of the rows that pass to out.
func filterByProcess(pipeline StreamPipeline, lbs []labels.Labels, selection []int, out []int) []int {
	for _, i := range selection {
		if _, _, ok := pipeline.Process(0, nil, lbs[i]); ok {
			out = append(out, i)
		}
	}
	return out
}

func reportPerEntry(b *testing.B, entries int) {
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(entries), "ns/entry")
}

// generateStructuredMetadata returns the same structured metadata as pairs, for
// the Arrow Map, and as labels.Labels, for the LabelsBuilder.
//
// Every entry has numLabels labels. The label under key sits in the middle of
// the entry, so a lookup has to skip about half of the pairs. It has value for
// matchPct percent of the entries, chosen pseudo-randomly with a fixed seed.
// The other labels and the values of non-matching entries differ from the
// searched ones in their first bytes, so the length and prefix check can reject
// them. They are as long as the searched key and value, so they are inline
// only if those are.
func generateStructuredMetadata(entries, numLabels, matchPct int, key, value string) ([][]metadataPair, []labels.Labels) {
	long := !arrow.IsViewInline(len(key)) || !arrow.IsViewInline(len(value))

	otherKey, otherValue, nonMatch := "label_%d", "v%d", "other-%d"
	if long {
		otherKey, otherValue, nonMatch = "label_with_a_long_name_%d", "value_that_is_long_%d", "other_value_that_is_long_%d"
	}

	rng := rand.New(rand.NewSource(1))
	rows := make([][]metadataPair, entries)
	lbs := make([]labels.Labels, entries)

	for i := range rows {
		pairs := make([]metadataPair, 0, numLabels)
		for l := 0; l < numLabels; l++ {
			if l == numLabels/2 {
				v := value
				if rng.Intn(100) >= matchPct {
					v = fmt.Sprintf(nonMatch, rng.Intn(10))
				}
				pairs = append(pairs, metadataPair{key, v})
				continue
			}
			pairs = append(pairs, metadataPair{fmt.Sprintf(otherKey, l), fmt.Sprintf(otherValue, rng.Intn(100))})
		}

		rows[i] = pairs
		b := labels.NewScratchBuilder(len(pairs))
		for _, p := range pairs {
			b.Add(p.key, p.value)
		}
		b.Sort()
		lbs[i] = b.Labels()
	}

	return rows, lbs
}
