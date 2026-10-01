package arrowfilter

import "github.com/prometheus/prometheus/util/pool"

// selectionPool pools the backing arrays for selection vectors ([]int32
// row-index lists) that Contains/ContainsNaive produce. Without it, every
// call allocates a fresh, worst-case-sized buffer (see scanContains) that's
// immediately superseded by the next stage's own call and left for GC -
// measured as a large share of allocated bytes once the columnar labels
// builder's own allocation costs were fixed (see pkg/logql/log's
// BenchmarkLogfmtLabelFilter_FullPipeline history).
//
// Buckets from 256 to ~1M int32s (1KB-4MB) cover a small per-line-field
// scan up to a large multi-megabyte block's row count.
var selectionPool = pool.New(1<<8, 1<<20, 2, func(size int) any {
	return make([]int32, size)
})

// GetSelection returns a []int32 with length 0 and capacity at least
// minCap, either freshly allocated or recycled from a previous
// PutSelection call. Safe to call with minCap <= 0 (returns nil).
func GetSelection(minCap int) []int32 {
	if minCap <= 0 {
		return nil
	}
	return selectionPool.Get(minCap).([]int32)[:0]
}

// PutSelection returns sel's backing array to the pool for reuse.
//
// The caller must not read or write sel after calling this, and must only
// call it once nothing else still holds a reference to the same backing
// array - e.g. an *ArrowBatch whose Selection field still points at sel and
// hasn't been superseded or released yet. A nil sel is a safe no-op.
func PutSelection(sel []int32) {
	if sel == nil {
		return
	}
	selectionPool.Put(sel)
}
