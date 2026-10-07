package log

import (
	"math/rand"
	"slices"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/require"
)

func newTestStringBatch(lines []string, selection []int) *ArrowBatch {
	sb := array.NewStringBuilder(memory.DefaultAllocator)
	defer sb.Release()
	for _, l := range lines {
		sb.Append(l)
	}
	return &ArrowBatch{LineColumn: sb.NewStringArray(), Selection: selection}
}

func TestContainsFilter_FilterBatch(t *testing.T) {
	for _, tc := range []struct {
		name            string
		match           string
		caseInsensitive bool
		lines           []string
		selection       []int
		expected        []int
	}{
		{
			name:      "dense",
			match:     "GC",
			lines:     []string{"abc", "xGCy", "GC", "zz"},
			selection: []int{0, 1, 2, 3},
			expected:  []int{1, 2},
		},
		{
			name:     "nil selection selects all rows",
			match:    "GC",
			lines:    []string{"abc", "xGCy", "GC", "zz"},
			expected: []int{1, 2},
		},
		{
			name:      "empty selection",
			match:     "GC",
			lines:     []string{"GC"},
			selection: []int{},
			expected:  []int{},
		},
		{
			name:      "sparse selection",
			match:     "GC",
			lines:     []string{"GC", "GC", "GC", "zz"},
			selection: []int{1, 3},
			expected:  []int{1},
		},
		{
			name:      "hit only in unselected rows",
			match:     "GC",
			lines:     []string{"GC", "zz", "GC", "zz"},
			selection: []int{1, 3},
			expected:  []int{},
		},
		{
			name:      "match at start and end of line",
			match:     "GC",
			lines:     []string{"GCx", "xGC", "GC"},
			selection: []int{0, 1, 2},
			expected:  []int{0, 1, 2},
		},
		{
			name:      "match straddling two lines is no match",
			match:     "GC",
			lines:     []string{"aG", "Cb", "zz"},
			selection: []int{0, 1, 2},
			expected:  []int{},
		},
		{
			name:      "straddling hit followed by real match in same row",
			match:     "GC",
			lines:     []string{"aG", "CbGC", "zz"},
			selection: []int{0, 1, 2},
			expected:  []int{1},
		},
		{
			name:      "multiple hits in one row are reported once",
			match:     "GC",
			lines:     []string{"GCGCGC", "zz"},
			selection: []int{0, 1},
			expected:  []int{0},
		},
		{
			name:      "empty lines",
			match:     "GC",
			lines:     []string{"", "GC", "", ""},
			selection: []int{0, 1, 2, 3},
			expected:  []int{1},
		},
		{
			name:      "match longer than every line",
			match:     "GCGCGC",
			lines:     []string{"GC", "GC", "GC"},
			selection: []int{0, 1, 2},
			expected:  []int{},
		},
		{
			name:      "empty match selects all selected rows",
			match:     "",
			lines:     []string{"a", "b", "c"},
			selection: []int{0, 2},
			expected:  []int{0, 2},
		},
		{
			name:            "case insensitive falls back",
			match:           "gc",
			caseInsensitive: true,
			lines:           []string{"abc", "xGCy", "gc", "zz"},
			selection:       []int{0, 1, 2, 3},
			expected:        []int{1, 2},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &containsFilter{match: []byte(tc.match), caseInsensitive: tc.caseInsensitive}
			b := newTestStringBatch(tc.lines, slices.Clone(tc.selection))

			// The result must agree with the scalar fallback. Compute it first
			// as FilterBatch reuses the selection of its input.
			want := filterLineByLine(b, f.Filter)

			got := f.FilterBatch(b)
			require.Equal(t, tc.expected, got.Selection)
			require.Equal(t, want.Selection, got.Selection)
		})
	}
}

func TestContainsFilter_FilterBatch_SlicedArray(t *testing.T) {
	b := newTestStringBatch([]string{"GC", "GC", "xGCy", "zz", "GC"}, nil)
	sliced := array.NewSlice(b.LineColumn, 2, 5).(*array.String) // "xGCy", "zz", "GC"
	b = &ArrowBatch{LineColumn: sliced, Selection: []int{0, 1, 2}}

	f := &containsFilter{match: []byte("GC")}
	require.Equal(t, []int{0, 2}, f.FilterBatch(b).Selection)
}

func TestContainsFilter_FilterBatch_Random(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []byte("aGC")
	matches := []string{"G", "GC", "aG", "GCa", "CaG", "GCGC"}

	for iter := 0; iter < 500; iter++ {
		lines := make([]string, rng.Intn(40))
		for i := range lines {
			line := make([]byte, rng.Intn(8))
			for j := range line {
				line[j] = alphabet[rng.Intn(len(alphabet))]
			}
			lines[i] = string(line)
		}

		var selection []int
		if rng.Intn(4) != 0 { // sometimes leave nil
			selection = []int{}
			for i := range lines {
				if rng.Intn(2) == 0 {
					selection = append(selection, i)
				}
			}
		}

		f := &containsFilter{match: []byte(matches[rng.Intn(len(matches))])}
		b := newTestStringBatch(lines, slices.Clone(selection))

		want := filterLineByLine(b, f.Filter).Selection
		got := f.FilterBatch(b).Selection
		require.Equal(t, want, got, "match=%q lines=%q selection=%v", f.match, lines, selection)
	}
}

func BenchmarkContainsFilter_FilterBatch(b *testing.B) {
	const (
		rows    = 10_000
		needle  = "GC pause"
		padding = "level=info ts=2026-10-07T12:00:00Z caller=server.go:123 msg=\"request handled\" duration=12ms status=200 path=/loki/api/v1/query_range"
	)

	for _, bc := range []struct {
		name       string
		hitEvery   int  // every n-th line contains the needle
		sparseRows bool // select only every other row
	}{
		{name: "rare_hits/dense", hitEvery: 100},
		{name: "rare_hits/sparse", hitEvery: 100, sparseRows: true},
		{name: "common_hits/dense", hitEvery: 2},
		{name: "common_hits/sparse", hitEvery: 2, sparseRows: true},
		{name: "no_hits/dense", hitEvery: 0},
	} {
		rng := rand.New(rand.NewSource(1))
		lines := make([]string, rows)
		for i := range lines {
			line := padding
			if bc.hitEvery > 0 && i%bc.hitEvery == 0 {
				pos := rng.Intn(len(padding))
				line = padding[:pos] + needle + padding[pos:]
			}
			lines[i] = line
		}

		// Throughput counts only the bytes of selected rows.
		selectedBytes := 0
		selection := make([]int, 0, rows)
		for i := range lines {
			if !bc.sparseRows || i%2 == 0 {
				selection = append(selection, i)
				selectedBytes += len(lines[i])
			}
		}

		f := &containsFilter{match: []byte(needle)}
		scratch := make([]int, len(selection))
		batch := newTestStringBatch(lines, slices.Clone(selection))

		// Both variants must agree before we compare their speed.
		require.Equal(b, filterLineByLine(batch, f.Filter).Selection, f.FilterBatch(batch).Selection)

		b.Run(bc.name+"/FilterBatch", func(b *testing.B) {
			b.SetBytes(int64(selectedBytes))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				// FilterBatch filters the selection in place, so restore it.
				copy(scratch, selection)
				batch.Selection = scratch
				_ = f.FilterBatch(batch)
			}
		})

		b.Run(bc.name+"/Filter", func(b *testing.B) {
			b.SetBytes(int64(selectedBytes))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				out := make([]int, 0, len(selection))
				for _, row := range selection {
					if f.Filter(unsafeGetBytes(batch.LineColumn.Value(row))) {
						out = append(out, row)
					}
				}
				_ = out
			}
		})
	}
}
