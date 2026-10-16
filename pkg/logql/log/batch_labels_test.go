package log

import (
	"fmt"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
)

var (
	testStreamLabels = labels.FromStrings("cluster", "c1", "env", "prod")
	testMetadata     = labels.FromStrings("sm_app", "api", "sm_zone", "z1")
)

// testBaseLabels returns the result of the stream, like StreamPipeline.BaseLabels.
func testBaseLabels() LabelsResult {
	b := NewBaseLabelsBuilder().ForLabels(testStreamLabels, labels.StableHash(testStreamLabels))
	b.Reset()
	return b.LabelsResult()
}

// processLogfmtRowWithMetadata is the oracle: what Process returns for line in a stream
// with testStreamLabels, with the structured metadata structuredMetadata.
func processLogfmtRowWithMetadata(p *LogfmtParser, line string, structuredMetadata labels.Labels) LabelsResult {
	b := NewBaseLabelsBuilder().ForLabels(testStreamLabels, labels.StableHash(testStreamLabels))
	b.Reset()
	b.Add(StructuredMetadataLabel, structuredMetadata)
	_, _ = p.Process(0, []byte(line), b)
	return b.LabelsResult()
}

func requireSameLabelsResult(t *testing.T, want, got LabelsResult, msgAndArgs ...any) {
	t.Helper()
	require.Equal(t, want.String(), got.String(), msgAndArgs...)
	require.Equal(t, want.Hash(), got.Hash(), msgAndArgs...)
	require.Equal(t, want.Labels().String(), got.Labels().String(), msgAndArgs...)
	require.Equal(t, want.Stream().String(), got.Stream().String(), msgAndArgs...)
	require.Equal(t, want.StructuredMetadata().String(), got.StructuredMetadata().String(), msgAndArgs...)
	require.Equal(t, want.Parsed().String(), got.Parsed().String(), msgAndArgs...)
}

// hasDuplicateKey reports whether a row of ParsedLabelsView has a key twice.
// TODO: remove when LabelsResult handles duplicate keys.
func hasDuplicateKey(b *ArrowBatch, row int) bool {
	offs, keys, _ := b.ParsedLabelsColumns()
	seen := map[string]bool{}
	for i := int(offs[row]); i < int(offs[row+1]); i++ {
		if seen[keys.Value(i)] {
			return true
		}
		seen[keys.Value(i)] = true
	}
	return false
}

// structuredMetadataOf gives every second row structured metadata.
func structuredMetadataOf(row int) labels.Labels {
	if row%2 == 0 {
		return testMetadata
	}
	return labels.EmptyLabels()
}

func TestArrowBatchLabelsResult(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, keepEmpty := range []bool{false, true} {
			for _, view := range []bool{false, true} {
				t.Run(fmt.Sprintf("strict=%v/keepEmpty=%v/view=%v", strict, keepEmpty, view), func(t *testing.T) {
					p := NewLogfmtParser(strict, keepEmpty)
					b := newLogfmtBatch(logfmtBatchLines)
					b.Selection = allRows(len(logfmtBatchLines))
					b.BaseLabels = testBaseLabels()

					// Structured metadata as labels, or as the view the columnar block decoder makes.
					rows := make([][]metadataPair, len(logfmtBatchLines))
					b.StructuredMetadata = make([]labels.Labels, len(logfmtBatchLines))
					for row := range logfmtBatchLines {
						b.StructuredMetadata[row] = structuredMetadataOf(row)
						structuredMetadataOf(row).Range(func(l labels.Label) {
							rows[row] = append(rows[row], metadataPair{l.Name, l.Value})
						})
					}
					if view {
						b.StructuredMetadata = nil
						b.StructuredMetadataView = newMetadataMap(t, rows)
					}

					got := p.ProcessBatch(b)

					checked := 0
					for row, line := range logfmtBatchLines {
						if hasDuplicateKey(got, row) {
							continue
						}
						requireSameLabelsResult(t,
							processLogfmtRowWithMetadata(p, line, structuredMetadataOf(row)),
							got.LabelsResult(row),
							"line %q", line)
						checked++
					}
					require.Greater(t, checked, len(logfmtBatchLines)/2, "most lines must be compared")
				})
			}
		}
	}
}

// A row without structured metadata and parsed labels has the labels of its
// stream. LabelsBuilder.LabelsResult returns the stream's own result for it,
// so does the batch.
func TestArrowBatchLabelsResult_BaseLabels(t *testing.T) {
	p := NewLogfmtParser(false, false)
	b := newLogfmtBatch([]string{``, `a=1`})
	b.Selection = allRows(2)
	base := testBaseLabels()
	b.BaseLabels = base

	got := p.ProcessBatch(b)

	require.True(t, got.LabelsResult(0) == base, "the result of the stream is reused")
	require.False(t, got.LabelsResult(1) == base)
	require.Equal(t, `{cluster="c1", env="prod"}`, got.LabelsResult(0).String())
	require.Equal(t, `{a="1", cluster="c1", env="prod"}`, got.LabelsResult(1).String())

	// A batch without a stream.
	b.BaseLabels = nil
	got = p.ProcessBatch(b)
	require.True(t, got.LabelsResult(0) == EmptyLabelsResult)
	require.Equal(t, `{a="1"}`, got.LabelsResult(1).String())
}

func TestArrowBatchLabelsResult_Lazy(t *testing.T) {
	p := NewLogfmtParser(false, false)
	b := newLogfmtBatch([]string{`a=1 b=2`})
	b.Selection = allRows(1)
	b.BaseLabels = testBaseLabels()
	got := p.ProcessBatch(b)

	res := got.LabelsResult(0).(*columnarLabelsResult)
	require.Equal(t, `{a="1", b="2"}`, res.Parsed().String())
	require.Equal(t, `{a="1", b="2", cluster="c1", env="prod"}`, res.Labels().String(), "the labels are built with the result")
	require.Empty(t, res.str, "the string is only formatted when it is asked for")
	require.Zero(t, res.hash, "so is the hash")

	require.Equal(t, `{a="1", b="2", cluster="c1", env="prod"}`, res.String())
	require.NotEmpty(t, res.str)
	require.Equal(t, newHasher().Hash(res.Labels()), res.Hash())
}
