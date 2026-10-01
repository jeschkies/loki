package log

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
)

func buildTestLineColumn(t *testing.T, lines []string) *array.String {
	t.Helper()
	bldr := array.NewStringBuilder(memory.DefaultAllocator)
	for _, l := range lines {
		bldr.Append(l)
	}
	col := bldr.NewStringArray()
	bldr.Release()
	t.Cleanup(col.Release)
	return col
}

func TestFindLogfmtValueSpan(t *testing.T) {
	line := []byte(`level=info method=GET msg="he said \"hi\"" path=/x empty= tail=end`)

	tests := []struct {
		key  string
		want string
		ok   bool
	}{
		{"level", "info", true},
		{"method", "GET", true},
		{"msg", `he said \"hi\"`, true}, // raw, still escaped - not unescaped
		{"path", "/x", true},
		{"empty", "", true},
		{"tail", "end", true},
		{"missing", "", false},
	}
	for _, tt := range tests {
		start, end, ok := findLogfmtValueSpan(line, []byte(tt.key))
		require.Equal(t, tt.ok, ok, "key %q", tt.key)
		if ok {
			require.Equal(t, tt.want, string(line[start:end]), "key %q", tt.key)
		}
	}
}

// TestFindLogfmtValueSpan_DuplicateKey documents current, intentional
// behavior: findLogfmtValueSpan returns the *first* occurrence of a
// repeated key, unlike LogfmtParser.Process's Set(), which overwrites on
// each occurrence and so ends up keeping the *last* one. Duplicate keys
// within one logfmt line are rare in practice; this is a known,
// deliberately unresolved divergence, same category as the List<Struct>
// path's analogous caveat (see FilterLogfmtLabelsColumn's doc comment).
func TestFindLogfmtValueSpan_DuplicateKey(t *testing.T) {
	line := []byte(`level=info level=warn level=error`)
	start, end, ok := findLogfmtValueSpan(line, []byte("level"))
	require.True(t, ok)
	require.Equal(t, "info", string(line[start:end]), "first occurrence wins, not last")
}

// TestFindLogfmtValueSpan_KeySubstringNeverFalseMatches checks the exact
// thing that needed a separate verify step in the List<Struct>+
// arrowfilter.Contains path (substring search flagging "loglevel" as a
// candidate for "level"): findLogfmtValueSpan compares whole tokens, so
// it can't have that false-positive class at all - no verify step needed
// here by construction.
func TestFindLogfmtValueSpan_KeySubstringNeverFalseMatches(t *testing.T) {
	line := []byte(`loglevel=info levels=other xlevel=y`)
	_, _, ok := findLogfmtValueSpan(line, []byte("level"))
	require.False(t, ok, "level is only ever a substring of the real keys here")
}

// TestFindLogfmtValueSpan_EscapedBackslashBeforeQuote checks that an
// escaped backslash immediately before the closing quote doesn't get
// mistaken for an escape of the quote itself (i.e. \\ followed by " must
// close the string, not be consumed as \" ).
func TestFindLogfmtValueSpan_EscapedBackslashBeforeQuote(t *testing.T) {
	line := []byte(`path="C:\\" next=ok`)
	start, end, ok := findLogfmtValueSpan(line, []byte("path"))
	require.True(t, ok)
	require.Equal(t, `C:\\`, string(line[start:end]))

	start2, end2, ok2 := findLogfmtValueSpan(line, []byte("next"))
	require.True(t, ok2)
	require.Equal(t, "ok", string(line[start2:end2]))
}

// TestFindLogfmtValueSpan_Malformed checks that malformed input never
// panics and is reported as not-found rather than matched incorrectly -
// same spirit as the ChunkFormatV6 lesson earlier this session: a
// hand-written decoder over untrusted input must fail safe, not crash.
func TestFindLogfmtValueSpan_Malformed(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"=",
		"key=",
		`key="unterminated`,
		`key="unterminated\`,
		`"`,
		`key=="double-equals"`,
		"=novalue",
		"key",
	}
	for _, c := range cases {
		require.NotPanics(t, func() {
			findLogfmtValueSpan([]byte(c), []byte("key"))
		}, "input %q", c)
	}
}

// TestEscapeLogfmtCompareValue_MultibyteUTF8 checks that valid multi-byte
// UTF-8 passes through unescaped, matching go-logfmt's own encoder (which
// only escapes ASCII control/quote/backslash bytes and leaves valid
// multi-byte sequences alone).
func TestEscapeLogfmtCompareValue_MultibyteUTF8(t *testing.T) {
	got := escapeLogfmtCompareValue([]byte("héllo wörld"))
	require.Equal(t, "héllo wörld", string(got))
}

func FuzzFindLogfmtValueSpan(f *testing.F) {
	seeds := []string{
		`level=info method=GET`,
		`level=info level=warn`,
		`loglevel=info levels=other`,
		`path="C:\\" next=ok`,
		`msg="he said \"hi\""`,
		"",
		"=",
		`key="unterminated`,
		`key="unterminated\`,
		"key==",
	}
	for _, s := range seeds {
		f.Add([]byte(s), []byte("level"))
	}
	f.Fuzz(func(t *testing.T, line, key []byte) {
		require.NotPanics(t, func() {
			start, end, ok := findLogfmtValueSpan(line, key)
			if ok {
				require.GreaterOrEqual(t, start, 0)
				require.LessOrEqual(t, end, len(line))
				require.LessOrEqual(t, start, end)
			}
		})
	})
}

func TestEscapeLogfmtCompareValue(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"info", "info"},                    // no quoting needed - unchanged
		{"he said \"hi\"", `he said \"hi\"`}, // quote forces escaping
		{"a b", "a b"},                       // space forces quoting, but no chars need escaping inside
		{"tab\there", `tab\there`},
	}
	for _, tt := range tests {
		got := escapeLogfmtCompareValue([]byte(tt.in))
		require.Equal(t, tt.want, string(got), "input %q", tt.in)
	}
}

func TestBuildAndFilterLogfmtValueView(t *testing.T) {
	lines := []string{
		"level=info method=GET",                       // 0: short inline value
		`level=warn msg="he said \"hi\""`,               // 1: level short inline, doesn't match "info"
		"level=info method=POST",                       // 2: matches
		"method=GET",                                   // 3: level key absent -> null
		`level="a value that is definitely over twelve bytes long" method=GET`, // 4: level value >12 bytes, buffer-referenced
	}
	col := buildTestLineColumn(t, lines)

	view := BuildLogfmtValueView(memory.DefaultAllocator, col, []byte("level"))
	defer view.Release()

	require.Equal(t, col.Len(), view.Len())
	require.False(t, view.IsNull(0))
	require.Equal(t, "info", view.Value(0))
	require.True(t, view.ValueHeader(0).IsInline(), "short value should be inline")

	require.False(t, view.IsNull(1))
	require.Equal(t, "warn", view.Value(1))

	require.False(t, view.IsNull(2))
	require.Equal(t, "info", view.Value(2))

	require.True(t, view.IsNull(3), "row 3 has no level key")

	require.False(t, view.IsNull(4))
	require.False(t, view.ValueHeader(4).IsInline(), "long value should reference the buffer, not be inline")
	require.Equal(t, "a value that is definitely over twelve bytes long", view.Value(4))

	// Filtering: escape-the-query, never unescape-the-data.
	wantEscaped := escapeLogfmtCompareValue([]byte("info"))
	rows := FilterLogfmtValueView(view, wantEscaped)
	require.Equal(t, []int32{0, 2}, rows)

	// A value that was quoted-with-escapes in the line: search for the
	// literal `he said "hi"` (real embedded quotes) - the escaped query
	// form must match the still-escaped raw bytes captured from the line.
	wantMsg := escapeLogfmtCompareValue([]byte(`he said "hi"`))
	require.Equal(t, `he said \"hi\"`, string(wantMsg))

	// Confirm long value's ViewHeader really references col's own buffer
	// (buffer index 0), not a copy - zero-copy claim, checked directly.
	hdr := view.ValueHeader(4)
	require.False(t, hdr.IsInline())
	require.EqualValues(t, 0, hdr.BufferIndex())
	lineStart := col.ValueOffsets()[4]
	// The value starts right after `level="` within row 4's line.
	expectedOffset := lineStart + int32(len(`level="`))
	require.Equal(t, expectedOffset, hdr.BufferOffset())
}

func TestBuildLogfmtValueView_EmptyColumn(t *testing.T) {
	col := buildTestLineColumn(t, nil)
	view := BuildLogfmtValueView(memory.DefaultAllocator, col, []byte("level"))
	defer view.Release()
	require.Equal(t, 0, view.Len())
}

func TestMaterializeLogfmtValueForRow(t *testing.T) {
	lines := []string{
		"level=info method=GET",
		`level=warn msg="he said \"hi\""`,
	}
	col := buildTestLineColumn(t, lines)
	view := BuildLogfmtValueView(memory.DefaultAllocator, col, []byte("level"))
	defer view.Release()

	base := labels.FromStrings("app", "bench")

	gotLbs := NewBaseLabelsBuilder().ForLabels(base, labels.StableHash(base))
	got := MaterializeLogfmtValueForRow(view, "level", 0, gotLbs)
	require.Equal(t, `{app="bench", level="info"}`, got.String())

	got2 := MaterializeLogfmtValueForRow(view, "level", 1, gotLbs)
	require.Equal(t, `{app="bench", level="warn"}`, got2.String())
}
