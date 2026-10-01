package log

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
)

// longMatch/longAdversarial are both well over the 12-byte inline limit,
// share the same length and first-4-byte prefix, and differ only in
// their tail - built this way (rather than two independently-chosen
// strings) specifically to stress tier 2's out-of-line verify step: a
// tier-1-only check (length+prefix) would wrongly accept longAdversarial.
const longMatch = "a-very-long-level-value-not-inlined-here"

var longAdversarial = longMatch[:4] + strings.Repeat("Z", len(longMatch)-4)

// buildLogfmtLinesSelectiveLong is buildLogfmtLinesSelective's
// non-inline counterpart: matching rows get longMatch (41 bytes) instead
// of "info", so the out-of-line buffer-reference path is exercised at
// batch scale, not just the single hand-written example above.
// Non-matching rows are split between a short value ("warn", exercises
// tier 1 rejecting on length alone) and longAdversarial (exercises tier
// 2 rejecting a same-length-same-prefix out-of-line candidate).
func buildLogfmtLinesSelectiveLong(n int, sel float64, seed int64) *array.String {
	r := rand.New(rand.NewSource(seed))
	bldr := array.NewStringBuilder(memory.DefaultAllocator)
	defer bldr.Release()
	bldr.Reserve(n)

	for i := 0; i < n; i++ {
		level := "warn"
		switch {
		case r.Float64() < sel:
			level = longMatch
		case r.Float64() < 0.1:
			level = longAdversarial
		}
		line := fmt.Sprintf("level=%s method=GET path=/api/v1/query duration=%dms", level, r.Intn(1000))
		bldr.Append(line)
	}
	return bldr.NewStringArray()
}

func TestBuildAndFilterPackedStringView(t *testing.T) {
	lines := []string{
		"level=info method=GET",                       // 0: short inline value
		`level=warn msg="he said \"hi\""`,              // 1: level short inline, doesn't match "info"
		"level=info method=POST",                       // 2: matches
		"method=GET",                                   // 3: level key absent -> null
		`level="a value that is definitely over twelve bytes long" method=GET`, // 4: >12 bytes, buffer-referenced
	}
	col := buildTestLineColumn(t, lines)

	view := BuildPackedStringView(memory.DefaultAllocator, col, []byte("level"), nil)
	defer view.Release()

	require.Equal(t, col.Len(), view.Len())
	require.False(t, view.IsNull(0))
	require.Equal(t, "info", string(view.Value(0)))
	require.True(t, view.Header(0).IsInline(), "short value should be inline")

	require.False(t, view.IsNull(1))
	require.Equal(t, "warn", string(view.Value(1)))

	require.False(t, view.IsNull(2))
	require.Equal(t, "info", string(view.Value(2)))

	require.True(t, view.IsNull(3), "row 3 has no level key")

	require.False(t, view.IsNull(4))
	require.False(t, view.Header(4).IsInline(), "long value should reference the buffer, not be inline")
	require.Equal(t, "a value that is definitely over twelve bytes long", string(view.Value(4)))

	wantEscaped := escapeLogfmtCompareValue([]byte("info"))
	for _, filter := range []func(*PackedStringView, []byte) []int32{FilterPackedStringView, FilterPackedStringViewSIMD} {
		rows := filter(view, wantEscaped)
		require.Equal(t, []int32{0, 2}, rows)

		// Exact length+prefix match but different full content must not
		// false-positive on the tier-1-only check: same length, same
		// prefix as row 4's value, different tail.
		longRows := filter(view, []byte("a value that is definitely over twelve bytes LONG"))
		require.Empty(t, longRows, "same length, same prefix, different tail must not match")

		matchLong := filter(view, []byte("a value that is definitely over twelve bytes long"))
		require.Equal(t, []int32{4}, matchLong)
	}
}

func TestBuildPackedStringView_EmptyColumn(t *testing.T) {
	col := buildTestLineColumn(t, nil)
	view := BuildPackedStringView(memory.DefaultAllocator, col, []byte("level"), nil)
	defer view.Release()
	require.Equal(t, 0, view.Len())
}

// TestBuildPackedStringView_Selection verifies that building with a
// selection scans only the named rows and produces a dense view whose
// row i matches a full (selection=nil) build's row selection[i] -
// exactly the property runFullPipelineBenchmark's PackedStringView-
// SIMD-Full variant depends on to skip findLogfmtValueSpan entirely
// for rows an earlier line filter already dropped, instead of
// scanning every row and intersecting afterward.
func TestBuildPackedStringView_Selection(t *testing.T) {
	lines := []string{
		"level=info method=GET",                                                // 0: short inline value
		`level=warn msg="he said \"hi\""`,                                       // 1: short inline, non-match
		"level=info method=POST",                                                // 2: matches
		"method=GET",                                                            // 3: level key absent -> null
		`level="a value that is definitely over twelve bytes long" method=GET`,  // 4: out-of-line
		"level=info extra=1",                                                    // 5: matches
	}
	col := buildTestLineColumn(t, lines)
	defer col.Release()

	full := BuildPackedStringView(memory.DefaultAllocator, col, []byte("level"), nil)
	defer full.Release()

	for _, selection := range [][]int32{
		{0, 2, 5},       // only matching rows
		{1, 3, 4},       // non-match, null, and out-of-line rows
		{3},             // single null row
		{},              // empty selection
		{0, 1, 2, 3, 4, 5}, // full selection, explicitly (not nil)
	} {
		t.Run(fmt.Sprint(selection), func(t *testing.T) {
			view := BuildPackedStringView(memory.DefaultAllocator, col, []byte("level"), selection)
			defer view.Release()

			require.Equal(t, len(selection), view.Len())
			for i, row := range selection {
				require.Equal(t, full.IsNull(int(row)), view.IsNull(i), "row %d (selection[%d])", row, i)
				if !full.IsNull(int(row)) {
					require.Equal(t, full.Value(int(row)), view.Value(i), "row %d (selection[%d])", row, i)
					require.Equal(t, full.Header(int(row)).IsInline(), view.Header(i).IsInline(), "row %d (selection[%d])", row, i)
				}
			}
		})
	}
}

// TestBuildPackedStringView_SelectionAgreesAtScale is
// TestBuildPackedStringView_Selection's randomized, larger-scale
// counterpart, covering both inline and non-inline values and several
// selection densities.
func TestBuildPackedStringView_SelectionAgreesAtScale(t *testing.T) {
	r := rand.New(rand.NewSource(11))

	for _, build := range []struct {
		name string
		col  *array.String
	}{
		{"inline", buildLogfmtLinesSelective(500, 0.3, 1)},
		{"non-inline", buildLogfmtLinesSelectiveLong(500, 0.3, 1)},
	} {
		t.Run(build.name, func(t *testing.T) {
			defer build.col.Release()

			full := BuildPackedStringView(memory.DefaultAllocator, build.col, []byte("level"), nil)
			defer full.Release()

			for _, density := range []float64{0.1, 0.5, 1.0} {
				t.Run(fmt.Sprintf("density=%.1f", density), func(t *testing.T) {
					var selection []int32
					for i := 0; i < build.col.Len(); i++ {
						if r.Float64() < density {
							selection = append(selection, int32(i))
						}
					}

					view := BuildPackedStringView(memory.DefaultAllocator, build.col, []byte("level"), selection)
					defer view.Release()

					require.Equal(t, len(selection), view.Len())
					for i, row := range selection {
						require.Equal(t, full.IsNull(int(row)), view.IsNull(i))
						if !full.IsNull(int(row)) {
							require.Equal(t, full.Value(int(row)), view.Value(i))
						}
					}
				})
			}
		})
	}
}

func TestMaterializePackedValueForRow(t *testing.T) {
	lines := []string{
		"level=info method=GET",
		`level=warn msg="he said \"hi\""`,
	}
	col := buildTestLineColumn(t, lines)
	view := BuildPackedStringView(memory.DefaultAllocator, col, []byte("level"), nil)
	defer view.Release()

	base := labels.FromStrings("app", "bench")

	gotLbs := NewBaseLabelsBuilder().ForLabels(base, labels.StableHash(base))
	got := MaterializePackedValueForRow(view, "level", 0, gotLbs)
	require.Equal(t, `{app="bench", level="info"}`, got.String())

	got2 := MaterializePackedValueForRow(view, "level", 1, gotLbs)
	require.Equal(t, `{app="bench", level="warn"}`, got2.String())
}

// TestPackedStringView_AgreesWithStringView cross-checks
// BuildPackedStringView/FilterPackedStringView/FilterPackedStringViewSIMD
// against the existing BuildLogfmtValueView/FilterLogfmtValueView
// (array.StringView-based) implementation, across selectivities and
// both inline (short) and non-inline (long, buffer-referenced) target
// values - this is the "validate the logic" step the SIMD wiring needed
// before trusting its numbers.
func TestPackedStringView_AgreesWithStringView(t *testing.T) {
	cases := []struct {
		name      string
		build     func(n int, sel float64, seed int64) *array.String
		wantPlain []byte
	}{
		{"inline", buildLogfmtLinesSelective, []byte("info")},
		{"non-inline", buildLogfmtLinesSelectiveLong, []byte(longMatch)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, sel := range []float64{1.0, 0.5, 0.1, 0.0} {
				lines := c.build(4096, sel, 11)
				defer lines.Release()

				wantEscaped := escapeLogfmtCompareValue(c.wantPlain)

				sv := BuildLogfmtValueView(memory.DefaultAllocator, lines, []byte("level"))
				wantRows := FilterLogfmtValueView(sv, wantEscaped)
				sv.Release()

				pv := BuildPackedStringView(memory.DefaultAllocator, lines, []byte("level"), nil)
				scalarRows := FilterPackedStringView(pv, wantEscaped)
				simdRows := FilterPackedStringViewSIMD(pv, wantEscaped)
				pv.Release()

				require.Equal(t, wantRows, scalarRows, "selectivity %.2f: StringView vs PackedStringView (scalar) disagree", sel)
				require.Equal(t, wantRows, simdRows, "selectivity %.2f: StringView vs PackedStringView (SIMD) disagree", sel)
			}
		})
	}
}
