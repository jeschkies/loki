// Package arrowfilter implements vectorized search operators over
// [arrow.RecordBatch] columns, shared by both the query engine executor
// (pkg/engine/internal/executor) and the classic per-line LogQL pipeline
// (pkg/logql/log).
package arrowfilter

import (
	"bytes"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow/array"
)

// ContainsNaive returns the indices of rows in col whose value contains
// needle, checking one row at a time via [bytes.Contains]. If selection is
// non-nil, only those row indices are checked and the result is a subset of
// selection; otherwise all rows in col are checked.
//
// ContainsNaive is the baseline row-by-row implementation that [Contains]
// is benchmarked against.
func ContainsNaive(col *array.String, needle []byte, selection []int32) []int32 {
	if selection != nil {
		out := make([]int32, 0, len(selection))
		for _, row := range selection {
			if col.IsValid(int(row)) && bytes.Contains(unsafeBytes(col.Value(int(row))), needle) {
				out = append(out, row)
			}
		}
		return out
	}

	out := make([]int32, 0, col.Len())
	for i := range col.Len() {
		if col.IsValid(i) && bytes.Contains(unsafeBytes(col.Value(i)), needle) {
			out = append(out, int32(i))
		}
	}
	return out
}

// Contains returns the indices of rows in col whose value contains needle.
// If selection is non-nil, the search is restricted to those row indices and
// the result is a subset of selection; otherwise all rows in col are
// searched.
//
// When selection is nil, Contains scans col's underlying, contiguous value
// buffer directly with [bytes.Index] and maps each hit back to a row via
// col's offsets, rather than calling a search function once per row. At low
// selectivity, a single scan can skip over many non-matching rows at once
// instead of paying per-row search overhead for each of them. When selection
// is non-nil (i.e. col has already been filtered down by an earlier stage),
// Contains falls back to the row-by-row approach, since the remaining rows
// are not necessarily contiguous in the value buffer.
func Contains(col *array.String, needle []byte, selection []int32) []int32 {
	if selection != nil {
		return ContainsNaive(col, needle, selection)
	}
	return scanContains(col, needle, bytes.Index)
}

// scanContains implements the buffer-wide scan shared by [Contains] and
// [ContainsSIMD]: only the underlying single-haystack search function
// (indexFunc) differs between them.
func scanContains(col *array.String, needle []byte, indexFunc func(haystack, needle []byte) int) []int32 {
	offsets := col.ValueOffsets()
	if len(offsets) == 0 {
		return nil
	}

	if len(needle) == 0 {
		out := make([]int32, col.Len())
		for i := range out {
			out[i] = int32(i)
		}
		return out
	}

	// ValueOffsets is not necessarily normalized to start at zero (e.g. col
	// may be a slice of a larger array), but ValueBytes always starts at the
	// first row's offset. Normalize so both can be indexed consistently.
	base := offsets[0]
	values := col.ValueBytes()

	nRows := len(offsets) - 1
	// Pre-size to the worst case (every row matches) so a high-selectivity
	// result never triggers append's incremental reallocate-and-copy growth.
	out := make([]int32, 0, nRows)
	pos, row := 0, 0

	for row < nRows {
		hit := indexFunc(values[pos:], needle)
		if hit == -1 {
			break
		}
		hitStart := pos + hit
		hitEnd := hitStart + len(needle)

		// Advance row to the one containing hitStart.
		for row < nRows && int(offsets[row+1]-base) <= hitStart {
			row++
		}
		if row >= nRows {
			break
		}

		rowEnd := int(offsets[row+1] - base)
		if hitEnd <= rowEnd {
			// The match is fully contained within row: a genuine hit. Resume
			// the search after this row, since we only need one match per
			// row and any later occurrence in it is redundant.
			if col.IsValid(row) {
				out = append(out, int32(row))
			}
			pos, row = rowEnd, row+1
			continue
		}

		// The match straddles this row's boundary into the next row, so it
		// isn't a real occurrence within a single row (rows are otherwise
		// unrelated data concatenated back-to-back in the value buffer).
		// Resume just past the start of this false hit rather than skipping
		// the row outright: row's own content still hasn't been checked.
		pos = hitStart + 1
	}

	return out
}

func unsafeBytes(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}
