package log

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
)

func TestFilterLogfmtLabelsColumn(t *testing.T) {
	lines := []string{
		"level=info method=GET",     // 0: real match
		"loglevel=info",             // 1: key is "loglevel", not "level" - Contains would flag it, verify must reject
		"level=information",        // 2: value is "information", not "info" - Contains would flag it, verify must reject
		"foo=bar level=info",        // 3: real match, key/value not at position 0
		"level=warn",                // 4: no match at all
		"level=info level=warn",     // 5: duplicate key - known caveat, see doc comment; matches today
	}

	lineBldr := array.NewStringBuilder(memory.DefaultAllocator)
	for _, l := range lines {
		lineBldr.Append(l)
	}
	lineCol := lineBldr.NewStringArray()
	lineBldr.Release()
	defer lineCol.Release()

	col := BuildLogfmtLabelsColumn(memory.DefaultAllocator, lineCol)
	defer col.Release()

	rows := FilterLogfmtLabelsColumn(col, []byte("level"), []byte("info"))
	require.Equal(t, []int32{0, 3, 5}, rows)
}

// TestMaterializeLogfmtLabelsForRow checks that materializing from the
// columnar extraction produces the exact same LabelsResult (same String(),
// same Hash()) as the current row-at-a-time LogfmtParser.Process path, for
// every row a real filter would keep.
func TestMaterializeLogfmtLabelsForRow(t *testing.T) {
	lines := []string{
		"level=info method=GET",
		"foo=bar level=info duration=12ms",
	}

	lineBldr := array.NewStringBuilder(memory.DefaultAllocator)
	for _, l := range lines {
		lineBldr.Append(l)
	}
	lineCol := lineBldr.NewStringArray()
	lineBldr.Release()
	defer lineCol.Release()

	col := BuildLogfmtLabelsColumn(memory.DefaultAllocator, lineCol)
	defer col.Release()

	base := labels.FromStrings("app", "bench")
	parser := NewLogfmtParser(false, false)

	for row, line := range lines {
		wantLbs := NewBaseLabelsBuilder().ForLabels(base, labels.StableHash(base))
		_, _ = parser.Process(0, []byte(line), wantLbs)
		want := wantLbs.LabelsResult()

		gotLbs := NewBaseLabelsBuilder().ForLabels(base, labels.StableHash(base))
		got := MaterializeLogfmtLabelsForRow(col, row, gotLbs)

		require.Equal(t, want.String(), got.String(), "row %d", row)
		require.Equal(t, want.Hash(), got.Hash(), "row %d", row)
	}
}

// TestBuildLogfmtLabelsColumnFiltered checks that restricting extraction to
// wantKeys still finds every match FilterLogfmtLabelsColumn would find
// against the unfiltered column, while dropping unrelated fields - and
// that scanning stops once every wanted key is seen (multiple occurrences
// of a non-wanted key, or fields after the wanted key, must not appear in
// the result).
func TestBuildLogfmtLabelsColumnFiltered(t *testing.T) {
	lines := []string{
		"level=info method=GET duration=12ms", // wanted key not last
		"method=GET level=info duration=12ms", // wanted key in the middle
		"level=warn method=GET",               // no match
		"method=GET duration=12ms",            // key absent entirely
	}

	lineBldr := array.NewStringBuilder(memory.DefaultAllocator)
	for _, l := range lines {
		lineBldr.Append(l)
	}
	lineCol := lineBldr.NewStringArray()
	lineBldr.Release()
	defer lineCol.Release()

	full := BuildLogfmtLabelsColumn(memory.DefaultAllocator, lineCol)
	defer full.Release()
	filtered := BuildLogfmtLabelsColumnFiltered(memory.DefaultAllocator, lineCol, [][]byte{[]byte("level")})
	defer filtered.Release()

	wantRows := FilterLogfmtLabelsColumn(full, []byte("level"), []byte("info"))
	gotRows := FilterLogfmtLabelsColumn(filtered, []byte("level"), []byte("info"))
	require.Equal(t, []int32{0, 1}, wantRows)
	require.Equal(t, wantRows, gotRows)

	// The filtered column must contain exactly one (level, ...) entry per
	// row that has the key at all, and nothing else.
	listArr := filtered.(*array.List)
	structArr := listArr.ListValues().(*array.Struct)
	keyCol := structArr.Field(0).(*array.String)
	offsets := listArr.Offsets()

	require.Equal(t, int32(1), offsets[1]-offsets[0], "row 0: exactly one entry")
	require.Equal(t, "level", keyCol.Value(int(offsets[0])))
	require.Equal(t, int32(1), offsets[2]-offsets[1], "row 1: exactly one entry")
	require.Equal(t, "level", keyCol.Value(int(offsets[1])))
	require.Equal(t, int32(1), offsets[3]-offsets[2], "row 2: level=warn still extracted once")
	require.Equal(t, int32(0), offsets[4]-offsets[3], "row 3: key absent, zero entries")
}
