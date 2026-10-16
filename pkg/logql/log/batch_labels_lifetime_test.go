//go:build !slicelabels

package log

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// This test does not run with the slicelabels build tag. ArrowBatch.LabelsResult relies on
// ScratchBuilder copying the strings of the columns, and slicelabels does not.

// The result must not refer to the columns, which are reused after Release.
func TestArrowBatchLabelsResult_OutlivesBatch(t *testing.T) {
	p := NewLogfmtParser(false, false)
	lines := []string{
		`a_rather_long_key_name_for_the_label=a_rather_long_value_that_cannot_be_inline short=1`,
		`s-k="e\nscaped long value that is not inline" b=2`,
	}
	b := newLogfmtBatch(lines)
	b.Selection = allRows(len(lines))
	b.BaseLabels = testBaseLabels()
	b.StructuredMetadataView = newMetadataMap(t, [][]metadataPair{{{"sm_long_key_name_here", "a long structured metadata value"}}, nil})

	got := p.ProcessBatch(b)
	results := []LabelsResult{got.LabelsResult(0), got.LabelsResult(1)}
	want := []string{results[0].String(), results[1].String()}
	parsed := []string{results[0].Parsed().String(), results[1].Parsed().String()}

	// Release the columns and overwrite the pooled memory with other data.
	got.ParsedLabelsView.Release()
	for i := 0; i < 5; i++ {
		other := newLogfmtBatch([]string{`zzzzzzzzzzzzzzzzzzzz=yyyyyyyyyyyyyyyyyyyyyyyyyyyy xxxxxxxxxxxxxxxx="w\nwwwwwwwwwwwwwwwwwwwwwwwwwwww"`, `q=r`})
		other.Selection = allRows(2)
		p.ProcessBatch(other).ParsedLabelsView.Release()
	}

	for i, r := range results {
		require.Equal(t, want[i], r.String())
		require.Equal(t, parsed[i], r.Parsed().String())
		require.NotContains(t, r.String(), "zzzz")
	}
}
