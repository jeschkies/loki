package log

import (
	"bytes"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/grafana/loki/v3/pkg/logql/log/logfmt"
)

// kvStructType is the element type of the naive logfmt labels column: one
// row per log line, each row a variable-length list of key/value string
// pairs.
//
// Measured (see BenchmarkLabelsColumn_NaiveVsCurrent): dictionary-encoding
// the key column was tried and reverted - even after fixing an allocation
// bug in the naive attempt, it was consistently ~1.2-1.3x slower than plain
// strings despite using less memory. The key vocabulary here is small and
// short (level, method, duration, ...), and hashing + memo-table lookup per
// key costs more than just copying those few bytes directly. Values are
// plain strings for the separate reason that their cardinality is
// unbounded and data-dependent.
var kvStructType = arrow.StructOf(
	arrow.Field{Name: "key", Type: arrow.BinaryTypes.String},
	arrow.Field{Name: "value", Type: arrow.BinaryTypes.String},
)

var equalSign = []byte("=")

// linesRawBytes returns the raw contiguous bytes backing every row in
// lines in one slice - lines.Data().Buffers()[2] is the array's own values
// buffer (see array.Binary.setData), already trimmed by the builder that
// produced lines to the bytes actually written.
func linesRawBytes(lines *array.String) []byte {
	buf := lines.Data().Buffers()[2]
	if buf == nil {
		return nil
	}
	return buf.Bytes()
}

// BuildLogfmtLabelsColumn parses every row in lines as logfmt and returns a
// List<Struct<key: Utf8, value: Utf8>> column, one list per row, in the
// same row order as lines.
//
// This is a first, unoptimized cut: reuses the existing logfmt.Decoder
// (byte-by-byte, no SIMD). No dictionary encoding, no buffer-sharing with
// lines' own values buffer. Keys/values are appended via
// StringBuilder.BinaryBuilder.Append([]byte) rather than
// StringBuilder.Append(string) - dec.Key()/dec.Value() already return
// []byte, and StringBuilder.Append(v string) does b.BinaryBuilder.
// Append([]byte(v)) internally, i.e. string(v) then []byte(v) right back -
// two allocated copies for data that started as []byte and ends up in a
// []byte values buffer either way. Same reasoning as
// decodeBlockToArrowBatch in pkg/chunkenc/batch.go.
func BuildLogfmtLabelsColumn(mem memory.Allocator, lines *array.String) arrow.Array {
	listBldr := array.NewListBuilder(mem, kvStructType)
	defer listBldr.Release()

	structBldr := listBldr.ValueBuilder().(*array.StructBuilder)
	keyBldr := structBldr.FieldBuilder(0).(*array.StringBuilder)
	valBldr := structBldr.FieldBuilder(1).(*array.StringBuilder)

	n := lines.Len()
	listBldr.Reserve(n)

	// Presize instead of letting the builders grow by repeated doubling
	// (measured as a large share of BuildLogfmtLabelsColumn's CPU and
	// allocated bytes). A real k/v pair always has exactly one unquoted
	// '=' plus possibly more inside a quoted value, so counting every '='
	// byte is a safe upper bound on the pair count - never an undercount,
	// same reasoning as decodeBlockToArrowBatch's uncompressedSize
	// over-reservation in pkg/chunkenc/batch.go. Total raw bytes is a
	// safe (loose) upper bound for both the key and value byte buffers,
	// since neither can exceed the lines they're extracted from.
	raw := linesRawBytes(lines)
	structBldr.Reserve(bytes.Count(raw, equalSign))
	keyBldr.BinaryBuilder.ReserveData(len(raw))
	valBldr.BinaryBuilder.ReserveData(len(raw))

	var dec logfmt.Decoder
	for i := 0; i < n; i++ {
		listBldr.Append(true)
		dec.Reset(unsafeGetBytes(lines.Value(i)))
		for dec.ScanKeyval() {
			structBldr.Append(true)
			keyBldr.BinaryBuilder.Append(dec.Key())
			valBldr.BinaryBuilder.Append(dec.Value())
		}
	}

	return listBldr.NewArray()
}

// BuildLogfmtLabelsColumnFiltered is BuildLogfmtLabelsColumn restricted to
// wantKeys: only key/value pairs whose key is one of wantKeys are appended
// to the resulting column - mirroring what ParserHint.ShouldExtract already
// does in the row-at-a-time LogfmtParser.Process (see parser.go), applied
// here to the columnar builder instead.
//
// Every row still needs a linear scan to find field boundaries - logfmt
// doesn't let you seek directly to a key - but scanning a row stops as
// soon as every key in wantKeys has been seen once, mirroring
// ParserHint.AllRequiredExtracted's early-exit. This both shrinks the
// resulting column (fewer struct entries - directly attacks the "many
// more bytes" cost this representation has shown throughout benchmarking)
// and reduces Arrow builder Append calls, one of the largest CPU costs
// measured for BuildLogfmtLabelsColumn.
func BuildLogfmtLabelsColumnFiltered(mem memory.Allocator, lines *array.String, wantKeys [][]byte) arrow.Array {
	listBldr := array.NewListBuilder(mem, kvStructType)
	defer listBldr.Release()

	structBldr := listBldr.ValueBuilder().(*array.StructBuilder)
	keyBldr := structBldr.FieldBuilder(0).(*array.StringBuilder)
	valBldr := structBldr.FieldBuilder(1).(*array.StringBuilder)

	n := lines.Len()
	listBldr.Reserve(n)

	// Tighter bound than BuildLogfmtLabelsColumn's: at most one entry per
	// row per wanted key, so no scan is needed to compute it.
	structBldr.Reserve(n * len(wantKeys))

	seen := make([]bool, len(wantKeys))
	var dec logfmt.Decoder
	for i := 0; i < n; i++ {
		listBldr.Append(true)
		dec.Reset(unsafeGetBytes(lines.Value(i)))
		for j := range seen {
			seen[j] = false
		}
		remaining := len(wantKeys)
		for remaining > 0 && dec.ScanKeyval() {
			key := dec.Key()
			for j, wk := range wantKeys {
				if !seen[j] && bytes.Equal(key, wk) {
					seen[j] = true
					remaining--
					structBldr.Append(true)
					keyBldr.BinaryBuilder.Append(key)
					valBldr.BinaryBuilder.Append(dec.Value())
					break
				}
			}
		}
	}

	return listBldr.NewArray()
}
