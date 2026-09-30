package arrowfilter

import (
	"math/rand"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/require"
)

// Test_buildRecordBatch is a smoke test confirming we can build and read a
// real arrow.RecordBatch with a string column using this repo's vendored
// apache/arrow-go, before relying on it for the search functions below.
func Test_buildRecordBatch(t *testing.T) {
	bldr := array.NewStringBuilder(memory.DefaultAllocator)
	defer bldr.Release()

	lines := []string{"hello world", "foo GET /bar", "baz"}
	for _, l := range lines {
		bldr.Append(l)
	}
	arr := bldr.NewStringArray()
	defer arr.Release()

	schema := arrow.NewSchema([]arrow.Field{{Name: "line", Type: arrow.BinaryTypes.String}}, nil)
	batch := array.NewRecordBatch(schema, []arrow.Array{arr}, int64(len(lines)))
	defer batch.Release()

	require.EqualValues(t, len(lines), batch.NumRows())
	col, ok := batch.Column(0).(*array.String)
	require.True(t, ok)
	for i, l := range lines {
		require.Equal(t, l, col.Value(i))
	}
}

func stringArray(t testing.TB, values []string) *array.String {
	t.Helper()
	bldr := array.NewStringBuilder(memory.DefaultAllocator)
	for _, v := range values {
		bldr.Append(v)
	}
	arr := bldr.NewStringArray()
	t.Cleanup(arr.Release)
	return arr
}

func TestContains(t *testing.T) {
	tt := []struct {
		name      string
		lines     []string
		needle    string
		selection []int32
		expect    []int32
	}{
		{
			name:   "no matches",
			lines:  []string{"foo", "bar", "baz"},
			needle: "GC",
			expect: nil,
		},
		{
			name:   "all match",
			lines:  []string{"aGCa", "GCbb", "ccGC"},
			needle: "GC",
			expect: []int32{0, 1, 2},
		},
		{
			name:   "some match, low selectivity",
			lines:  []string{"foo", "has GC here", "bar", "baz", "also GC"},
			needle: "GC",
			expect: []int32{1, 4},
		},
		{
			name: "needle straddles a row boundary: must not be a false positive",
			// "G" ends row 0, "C" starts row 1: concatenated buffer contains
			// "GC" but neither row individually contains it.
			lines:  []string{"aaaG", "Caaa"},
			needle: "GC",
			expect: nil,
		},
		{
			name: "genuine match still found after a boundary-straddling false hit",
			// Row 0 ends in "G", row 1 starts with "C" (false hit spanning
			// the boundary), but row 1 also genuinely contains "GC" later.
			lines:  []string{"aaaG", "CaaGCbb"},
			needle: "GC",
			expect: []int32{1},
		},
		{
			name:   "empty needle matches everything",
			lines:  []string{"foo", "bar", ""},
			needle: "",
			expect: []int32{0, 1, 2},
		},
		{
			name:      "restricted to selection",
			lines:     []string{"has GC", "no match", "also GC", "has GC too"},
			needle:    "GC",
			selection: []int32{1, 2},
			expect:    []int32{2},
		},
		{
			name:   "multiple occurrences within one row count once",
			lines:  []string{"GC and GC and GC"},
			needle: "GC",
			expect: []int32{0},
		},
		{
			name:   "needle longer than any row",
			lines:  []string{"a", "bb", "ccc"},
			needle: "much too long",
			expect: nil,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			col := stringArray(t, tc.lines)

			naive := ContainsNaive(col, []byte(tc.needle), tc.selection)
			require.ElementsMatch(t, tc.expect, naive, "ContainsNaive")

			scan := Contains(col, []byte(tc.needle), tc.selection)
			require.ElementsMatch(t, tc.expect, scan, "Contains")
		})
	}
}

// TestContains_MatchesNaive checks Contains and ContainsNaive agree on
// random data, including lines constructed to straddle row boundaries.
func TestContains_MatchesNaive(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	alphabet := []byte("ABCabc GC")

	randLine := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[r.Intn(len(alphabet))]
		}
		return string(b)
	}

	for trial := range 200 {
		nLines := 1 + r.Intn(20)
		lines := make([]string, nLines)
		for i := range lines {
			lines[i] = randLine(r.Intn(12))
		}
		col := stringArray(t, lines)

		naive := ContainsNaive(col, []byte("GC"), nil)
		scan := Contains(col, []byte("GC"), nil)
		require.ElementsMatch(t, naive, scan, "trial %d: lines=%q", trial, lines)

		if len(naive) > 0 {
			// Re-filtering an existing selection must also agree.
			naive2 := ContainsNaive(col, []byte("C"), naive)
			scan2 := Contains(col, []byte("C"), naive)
			require.ElementsMatch(t, naive2, scan2, "trial %d selection rerun: lines=%q", trial, lines)
		}
	}
}

// TestContainsSIMD_MatchesContains checks ContainsSIMD agrees with Contains
// (and therefore with ContainsNaive) on random data. Line lengths
// deliberately span both sides of go-memmem's 32-byte MIN_HAYSTACK and its
// separate scalar tail path, since that boundary is the most likely place
// for a hand-written SIMD routine to diverge.
//
// go-memmem v0.1.0 had a tail-read bug here (see the investigation in
// github.com/jeschkies/go-memmem, fixed in v0.2.0 by be6c9df/f9dd68a). This
// test used to be skipped by default while that was unresolved; now that
// go.mod pins v0.2.0, it runs for real.
func TestContainsSIMD_MatchesContains(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	alphabet := []byte("ABCabc GC")

	randLine := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[r.Intn(len(alphabet))]
		}
		return string(b)
	}

	var mismatches int
	for trial := range 500 {
		nLines := 1 + r.Intn(20)
		lines := make([]string, nLines)
		for i := range lines {
			// 0-95 bytes: spans well below, right around, and well above the
			// 32-byte MIN_HAYSTACK threshold.
			lines[i] = randLine(r.Intn(96))
		}
		col := stringArray(t, lines)

		want := Contains(col, []byte("GC"), nil)
		got := ContainsSIMD(col, []byte("GC"), nil)
		if !elementsMatch(want, got) {
			mismatches++
			t.Logf("trial %d MISMATCH: lines=%q want=%v got=%v", trial, lines, want, got)
		}
	}

	if mismatches > 0 {
		t.Errorf("ContainsSIMD disagreed with Contains on %d/500 trials (see logs above)", mismatches)
	}
}

func elementsMatch(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[int32]int, len(a))
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
	}
	for _, c := range seen {
		if c != 0 {
			return false
		}
	}
	return true
}
