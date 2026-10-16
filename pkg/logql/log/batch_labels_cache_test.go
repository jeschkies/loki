package log

import (
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
)

// parsedBatch returns the batch of the lines after the logfmt stage.
func parsedBatch(t *testing.T, lines []string, metadata []labels.Labels) *ArrowBatch {
	t.Helper()
	b := newLogfmtBatch(lines)
	b.Selection = allRows(len(lines))
	b.BaseLabels = testBaseLabels()
	b.StructuredMetadata = metadata
	return NewLogfmtParser(false, false).ProcessBatch(b)
}

func TestArrowBatchLabelsResult_CacheHit(t *testing.T) {
	got := parsedBatch(t, []string{
		`a=1 b=2`, // 0
		`a=1 b=2`, // 1: the same pairs
		`a=1 b=3`, // 2: another value
		`b=2 a=1`, // 3: the same labels in another order
	}, nil)

	r := make([]LabelsResult, 4)
	for i := range r {
		r[i] = got.LabelsResult(i)
	}

	require.True(t, r[0] == r[1], "equal pairs share the result")
	require.False(t, r[0] == r[2])
	require.False(t, r[0] == r[3], "the order is part of the key")
	require.Equal(t, 3, got.LabelsCache.Len())

	for i, want := range []string{
		`{a="1", b="2", cluster="c1", env="prod"}`,
		`{a="1", b="2", cluster="c1", env="prod"}`,
		`{a="1", b="3", cluster="c1", env="prod"}`,
		`{a="1", b="2", cluster="c1", env="prod"}`,
	} {
		require.Equal(t, want, r[i].String(), "row %d", i)
	}
}

// The string is formatted once for all rows that share a result.
func TestArrowBatchLabelsResult_CacheSharesString(t *testing.T) {
	got := parsedBatch(t, []string{`a=1`, `a=1`}, nil)

	first := got.LabelsResult(0).(*columnarLabelsResult)
	require.Empty(t, first.str, "not formatted yet")
	_ = first.String()

	second := got.LabelsResult(1).(*columnarLabelsResult)
	require.NotEmpty(t, second.str, "the string of the first row is already there")
	require.Equal(t, first.Hash(), second.Hash())
}

// The same pairs in another category must not hit.
func TestArrowBatchLabelsResult_CacheSeparatesCategories(t *testing.T) {
	got := parsedBatch(t,
		[]string{``, `k=1`},
		[]labels.Labels{labels.FromStrings("k", "1"), labels.EmptyLabels()})

	metadata, parsed := got.LabelsResult(0), got.LabelsResult(1)

	require.False(t, metadata == parsed)
	require.Equal(t, `{k="1"}`, metadata.StructuredMetadata().String())
	require.True(t, metadata.Parsed().IsEmpty())
	require.Equal(t, `{k="1"}`, parsed.Parsed().String())
	require.True(t, parsed.StructuredMetadata().IsEmpty())
	require.Equal(t, metadata.String(), parsed.String(), "the labels are the same, the categories are not")
}

func TestArrowBatchLabelsResult_CacheKeyIncludesMetadata(t *testing.T) {
	got := parsedBatch(t,
		[]string{`a=1`, `a=1`, `a=1`},
		[]labels.Labels{labels.FromStrings("sm", "x"), labels.FromStrings("sm", "y"), labels.FromStrings("sm", "x")})

	r0, r1, r2 := got.LabelsResult(0), got.LabelsResult(1), got.LabelsResult(2)

	require.False(t, r0 == r1, "another structured metadata")
	require.True(t, r0 == r2)
	require.Equal(t, `{sm="x"}`, r0.StructuredMetadata().String())
	require.Equal(t, `{sm="y"}`, r1.StructuredMetadata().String())
}

// A row without pairs has the labels of the stream, whatever is in the cache.
func TestArrowBatchLabelsResult_CacheNoPairs(t *testing.T) {
	got := parsedBatch(t, []string{``, `a=1`, ``}, nil)

	require.True(t, got.LabelsResult(0) == got.BaseLabels)
	_ = got.LabelsResult(1)
	require.True(t, got.LabelsResult(2) == got.BaseLabels)
	require.Equal(t, 1, got.LabelsCache.Len(), "the stream's own result is not cached")
}

func TestArrowBatchLabelsResult_CacheLimit(t *testing.T) {
	got := parsedBatch(t, []string{`a=1`, `a=2`, `a=3`, `a=1`, `a=3`, `a=3`}, nil)
	got.LabelsCache = NewLabelsResultCache(2)

	r := make([]LabelsResult, 6)
	for i := range r {
		r[i] = got.LabelsResult(i)
	}

	require.Equal(t, 2, got.LabelsCache.Len(), "it stops storing when it is full")
	require.True(t, r[0] == r[3], "the stored ones still hit")
	require.False(t, r[4] == r[5], "a set that was not stored is built again")
	for i, want := range []string{`a="1"`, `a="2"`, `a="3"`, `a="1"`, `a="3"`, `a="3"`} {
		require.Contains(t, r[i].String(), want, "row %d is correct either way", i)
	}
}

func TestArrowBatchLabelsResult_CacheOff(t *testing.T) {
	got := parsedBatch(t, []string{`a=1`, `a=1`}, nil)
	got.LabelsCache = NewLabelsResultCache(0)

	r0, r1 := got.LabelsResult(0), got.LabelsResult(1)

	require.False(t, r0 == r1)
	require.Equal(t, r0.String(), r1.String())
	require.Zero(t, got.LabelsCache.Len())
}

// A cache that lives longer than a batch gives the later batches the results of the
// earlier ones, and those must stay valid when the earlier batch is released.
func TestArrowBatchLabelsResult_CacheSharedBetweenBatches(t *testing.T) {
	cache := NewLabelsResultCache(10)

	first := parsedBatch(t, []string{`a_rather_long_key_name=a_rather_long_value_that_cannot_be_inline`}, nil)
	first.LabelsCache = cache
	r1 := first.LabelsResult(0)
	want := r1.String()
	first.ParsedLabelsView.Release()

	// The memory of the first batch is overwritten by other batches.
	for i := 0; i < 5; i++ {
		other := parsedBatch(t, []string{`zzzzzzzzzzzzzzzzzzzzzzzz=yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy`}, nil)
		other.ParsedLabelsView.Release()
	}

	second := parsedBatch(t, []string{`a_rather_long_key_name=a_rather_long_value_that_cannot_be_inline`}, nil)
	second.LabelsCache = cache
	r2 := second.LabelsResult(0)

	require.True(t, r1 == r2, "a hit across batches")
	require.Equal(t, want, r2.String())
	require.NotContains(t, r2.String(), "zzzz")
}

func BenchmarkLabelsKey(b *testing.B) {
	lines := generateLevelLines(1000, 20, false, 100, false)
	batch := newLogfmtBatch(lines)
	batch.Selection = allRows(len(lines))
	out := NewLogfmtParser(false, false).ProcessBatch(batch)
	defer out.ParsedLabelsView.Release()

	b.ReportAllocs()
	b.ResetTimer()
	var sink uint64
	for i := 0; i < b.N; i++ {
		key, _ := out.labelsKey(i % len(lines))
		sink += key
	}
	labelsSink += int(sink & 1)
}
