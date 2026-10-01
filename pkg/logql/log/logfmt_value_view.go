package log

import (
	"bytes"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/grafana/loki/v3/pkg/arrowfilter"
)

// needsLogfmtQuoting mirrors go-logfmt's own encoder
// (vendor/github.com/go-logfmt/logfmt/encode.go:needsQuotedValueRune): a
// value is written quoted only if it contains one of these bytes. All
// trigger bytes are ASCII, so byte-level scanning agrees with the real
// encoder's rune-level scanning - a multi-byte UTF-8 continuation byte
// (always >= 0x80) never matches any of these.
func needsLogfmtQuoting(b []byte) bool {
	for _, c := range b {
		if c <= ' ' || c == '=' || c == '"' || c == 0x7f {
			return true
		}
	}
	return false
}

// escapeLogfmtCompareValue returns the bytes lit would be written as
// *inside the quotes* of a logfmt-encoded quoted value, mirroring
// go-logfmt's writeQuotedString escaping exactly (vendor/github.com/
// go-logfmt/logfmt/jsonstring.go). If lit doesn't need quoting at all,
// it's returned unchanged - that's the raw, unescaped form a conformant
// encoder would have written.
//
// This runs once per query, on the filter's literal - not per row. The
// direction is escape-the-query, not unescape-the-data: FindLogfmtValueSpan
// and BuildLogfmtValueView never call logfmt's unquoteBytes, so a value's
// bytes stay genuinely zero-copy (referenceable via StringView) even when
// it was originally quoted-with-escapes.
//
// Caveat: only handles valid UTF-8 input (true for any Go string literal
// from a real query) - doesn't reproduce the real encoder's �
// replacement for invalid UTF-8, since that can't arise from a query
// literal.
func escapeLogfmtCompareValue(lit []byte) []byte {
	if !needsLogfmtQuoting(lit) {
		return lit
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 0, len(lit)+2)
	for _, b := range lit {
		switch {
		case b == '\\' || b == '"':
			out = append(out, '\\', b)
		case b == '\n':
			out = append(out, '\\', 'n')
		case b == '\r':
			out = append(out, '\\', 'r')
		case b == '\t':
			out = append(out, '\\', 't')
		case b < 0x20 || b == 0x7f:
			out = append(out, '\\', 'u', '0', '0', hex[b>>4], hex[b&0xf])
		default:
			out = append(out, b)
		}
	}
	return out
}

// findLogfmtValueSpan scans line for key and returns the byte range
// [start, end) of its value exactly as written in line - the raw content
// inside the surrounding quotes if the value is quoted (escaped or not),
// or the bare unquoted span otherwise. It never calls logfmt's
// unquoteBytes: unlike logfmt.Decoder.ScanKeyval, it only needs to find
// where a value is, not what it decodes to.
//
// Mirrors pkg/logql/log/logfmt/decode.go's grammar (key, '=', value or
// quoted value) closely enough to walk past non-matching keys' values
// correctly, including ones containing escaped quotes. Returns ok=false
// if key isn't found, or the line is malformed before it could be.
func findLogfmtValueSpan(line, key []byte) (start, end int, ok bool) {
	n := len(line)
	pos := 0
	for pos < n {
		for pos < n && line[pos] <= ' ' {
			pos++
		}
		if pos >= n {
			return 0, 0, false
		}

		keyStart := pos
		for pos < n && line[pos] > ' ' && line[pos] != '=' && line[pos] != '"' {
			pos++
		}
		if pos == keyStart {
			// line[pos] is '=' or '"' right where a key was expected (e.g.
			// a bare "=novalue" or a lone '"') - not parseable as a key.
			// Skip the byte so pos always advances; without this, the
			// outer loop spins forever re-finding the same empty key.
			pos++
			continue
		}
		isMatch := bytes.Equal(line[keyStart:pos], key)

		if pos >= n || line[pos] != '=' {
			// Bare key with no value - move on to the next token.
			continue
		}
		pos++ // skip '='

		if pos >= n || line[pos] <= ' ' {
			// Empty value.
			if isMatch {
				return pos, pos, true
			}
			continue
		}

		if line[pos] == '"' {
			valStart := pos + 1
			p := valStart
			esc, closed := false, false
			for p < n {
				c := line[p]
				switch {
				case esc:
					esc = false
				case c == '\\':
					esc = true
				case c == '"':
					closed = true
				}
				if closed {
					break
				}
				p++
			}
			if !closed {
				return 0, 0, false
			}
			if isMatch {
				return valStart, p, true
			}
			pos = p + 1
			continue
		}

		valStart := pos
		for pos < n && line[pos] > ' ' {
			pos++
		}
		if isMatch {
			return valStart, pos, true
		}
	}
	return 0, 0, false
}

// BuildLogfmtValueView builds a StringView column holding, for each row in
// lines, the raw byte span of key's value as it appears in that row's
// line - or null if key isn't present in that row.
//
// Values up to 12 bytes are stored fully inline in the ViewHeader itself
// (no buffer reference at all - most logfmt values are this short).
// Longer values reference lines' own underlying buffer directly (buffer
// index 0 in the returned array's variadic buffer list), via
// array.NewData's buffer retain/release, the same zero-copy sharing
// mechanism used elsewhere on this branch. Either way, the bytes stored
// are exactly what findLogfmtValueSpan found - never unescaped.
func BuildLogfmtValueView(mem memory.Allocator, lines *array.String, key []byte) *array.StringView {
	n := lines.Len()

	headerBuf := memory.NewResizableBuffer(mem)
	headerBuf.Resize(arrow.ViewHeaderTraits.BytesRequired(n))
	headers := arrow.ViewHeaderTraits.CastFromBytes(headerBuf.Bytes())

	validity := make([]byte, (n+7)/8)
	nullCount := 0

	lineValues := lines.Data().Buffers()[2]

	for i := 0; i < n; i++ {
		line := unsafeGetBytes(lines.Value(i))
		start, end, ok := findLogfmtValueSpan(line, key)
		if !ok {
			nullCount++
			continue
		}
		validity[i/8] |= 1 << (i % 8)

		lineStart := lines.ValueOffsets()[i]
		length := end - start

		hdr := &headers[i]
		hdr.SetBytes(line[start:end])
		if !arrow.IsViewInline(length) {
			hdr.SetIndexOffset(0, int32(lineStart)+int32(start))
		}
	}

	validityBuf := memory.NewBufferBytes(validity)
	data := array.NewData(arrow.BinaryTypes.StringView, n,
		[]*memory.Buffer{validityBuf, headerBuf, lineValues}, nil, nullCount, 0)
	defer data.Release()
	return array.NewStringViewData(data)
}

// MaterializeLogfmtValueForRow builds the final LabelsResult for row,
// given view's value for that row under label name key - mirroring
// MaterializeLogfmtLabelsForRow's contract (pkg/logql/log/
// labels_column_filter.go): only ever meant to be called for rows that
// survived a filter, so the sort+hash+categorize+String cost is paid
// only for rows that matter.
func MaterializeLogfmtValueForRow(view *array.StringView, key string, row int, lbs *LabelsBuilder) LabelsResult {
	lbs.Reset()
	lbs.Set(ParsedLabel, key, view.Value(row))
	return lbs.LabelsResult()
}

// FilterLogfmtValueView returns the row indices in view whose value
// equals wantEscaped - the pre-escaped comparison form from
// escapeLogfmtCompareValue, compared as-is against view's raw (never
// unescaped) bytes.
//
// A plain per-row equality check: ViewHeader's per-element
// inline-or-indirect layout doesn't fit arrowfilter's buffer-wide SIMD
// scan, which assumes one uniform contiguous values buffer - the thing
// StringView deliberately isn't.
func FilterLogfmtValueView(view *array.StringView, wantEscaped []byte) []int32 {
	n := view.Len()
	rows := arrowfilter.GetSelection(n)
	for i := 0; i < n; i++ {
		if view.IsNull(i) {
			continue
		}
		if bytes.Equal(unsafeGetBytes(view.Value(i)), wantEscaped) {
			rows = append(rows, int32(i))
		}
	}
	return rows
}
