package log

import (
	"bytes"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"

	"github.com/grafana/loki/v3/pkg/arrowfilter"
)

// FilterLogfmtLabelsColumn finds every row in col (a
// List<Struct<key: Utf8, value: Utf8>> as produced by
// BuildLogfmtLabelsColumn) that has at least one key/value pair equal to
// (wantKey, wantValue), and returns the matching row indices in ascending
// order.
//
// Matching runs in two phases:
//
//  1. Candidate generation: arrowfilter.Contains (SIMD substring search)
//     scans the flat key and value columns independently for wantKey and
//     wantValue, producing two candidate position lists over the *same*
//     flat (key,value)-pair index space - cheap, vectorized, over every
//     pair in the batch.
//
//  2. Verify: Contains is substring search, not equality - "loglevel"
//     contains "level", and "information" contains "info" - so a raw
//     candidate hit doesn't mean an exact match, and a position appearing
//     in both candidate lists only means key and value *each* contain
//     their literal somewhere, not necessarily as a whole-value match.
//     Candidates are intersected positionally (same flat index in both
//     lists), then each surviving position gets a real byte-equality
//     check before counting as a match. This verify pass only touches the
//     (much smaller) candidate set, not every pair in the batch, so it
//     stays cheap even though it isn't vectorized.
//
// Known caveat: logfmt allows a key to repeat within one line
// (`level=warn level=info`); real LabelsBuilder.Set semantics is
// last-value-wins, but this function matches a row as soon as *any* of its
// pairs equals (wantKey, wantValue), regardless of position. Duplicate keys
// within one line are rare in practice; this is a known, not yet handled,
// divergence from Set's semantics.
func FilterLogfmtLabelsColumn(col arrow.Array, wantKey, wantValue []byte) []int32 {
	listArr := col.(*array.List)
	structArr := listArr.ListValues().(*array.Struct)
	keyCol := structArr.Field(0).(*array.String)
	valCol := structArr.Field(1).(*array.String)
	offsets := listArr.Offsets()

	keyCandidates := arrowfilter.Contains(keyCol, wantKey, nil)
	valCandidates := arrowfilter.Contains(valCol, wantValue, nil)

	// keyCandidates and valCandidates are both ascending int32 lists over
	// the same flat index space - a merge-join intersection finds
	// candidates present in both in O(len(keyCandidates)+len(valCandidates)).
	verified := arrowfilter.GetSelection(min(len(keyCandidates), len(valCandidates)))
	i, j := 0, 0
	for i < len(keyCandidates) && j < len(valCandidates) {
		switch {
		case keyCandidates[i] < valCandidates[j]:
			i++
		case keyCandidates[i] > valCandidates[j]:
			j++
		default:
			idx := keyCandidates[i]
			if bytes.Equal(unsafeGetBytes(keyCol.Value(int(idx))), wantKey) &&
				bytes.Equal(unsafeGetBytes(valCol.Value(int(idx))), wantValue) {
				verified = append(verified, idx)
			}
			i++
			j++
		}
	}
	// keyCandidates/valCandidates never escape this function - safe to
	// return them now, nothing reads them again.
	arrowfilter.PutSelection(keyCandidates)
	arrowfilter.PutSelection(valCandidates)

	// Map verified flat indices back to row indices. offsets and verified
	// are both ascending, so a two-pointer walk avoids a per-candidate
	// binary search.
	rows := make([]int32, 0, len(verified))
	row := 0
	for _, idx := range verified {
		for offsets[row+1] <= idx {
			row++
		}
		if len(rows) == 0 || rows[len(rows)-1] != int32(row) {
			rows = append(rows, int32(row))
		}
	}
	arrowfilter.PutSelection(verified)

	return rows
}

// MaterializeLogfmtLabelsForRow builds the final LabelsResult for row of
// col (as produced by BuildLogfmtLabelsColumn) - the form the metrics
// engine and iterators actually consume. It replays row's already-extracted
// key/value pairs into lbs via Set(ParsedLabel, ...), exactly as
// LogfmtParser.Process does today, then calls the existing
// LabelsBuilder.LabelsResult() (sort + hash + categorize + String) - no new
// materialization logic, just fed from the columnar extraction instead of
// re-parsing the line. lbs is reset internally; callers reuse one lbs
// across rows the same way runRemainingStagesPerRow does.
//
// This is only ever meant to be called for rows that survived a filter
// (e.g. FilterLogfmtLabelsColumn's result) - that's what makes the
// columnar approach cheaper end to end: the row-at-a-time path pays this
// same LabelsResult cost unconditionally per row that reaches it, while
// here it's paid only for the rows that actually matter.
func MaterializeLogfmtLabelsForRow(col arrow.Array, row int, lbs *LabelsBuilder) LabelsResult {
	listArr := col.(*array.List)
	structArr := listArr.ListValues().(*array.Struct)
	keyCol := structArr.Field(0).(*array.String)
	valCol := structArr.Field(1).(*array.String)
	offsets := listArr.Offsets()

	lbs.Reset()
	for i := offsets[row]; i < offsets[row+1]; i++ {
		lbs.Set(ParsedLabel, keyCol.Value(int(i)), valCol.Value(int(i)))
	}
	return lbs.LabelsResult()
}
