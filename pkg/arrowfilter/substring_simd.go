package arrowfilter

import (
	"github.com/apache/arrow-go/v18/arrow/array"
	memmem "github.com/jeschkies/go-memmem/pkg/search"
)

// ContainsSIMD is identical to [Contains] except it uses
// [github.com/jeschkies/go-memmem]'s SIMD-accelerated Index instead of the
// standard library's [bytes.Index] for the underlying single-haystack
// search.
//
// go-memmem v0.1.0 had a tail-read bug for short haystacks that made this
// unsafe to use; go.mod now pins v0.2.0, which fixes it (see be6c9df and
// f9dd68a upstream), and TestContainsSIMD_MatchesContains runs for real
// (no longer skipped) to confirm it stays that way.
func ContainsSIMD(col *array.String, needle []byte, selection []int32) []int32 {
	if selection != nil {
		return ContainsNaive(col, needle, selection)
	}
	return scanContains(col, needle, memmemIndex)
}

func memmemIndex(haystack, needle []byte) int {
	return int(memmem.Index(haystack, needle))
}
