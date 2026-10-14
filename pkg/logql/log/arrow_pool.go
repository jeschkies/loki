package log

import (
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/prometheus/prometheus/util/pool"
)

// arrowBufferPool pools the byte slices behind the Arrow buffers of batches:
// headers, offsets and the data of columns that are built by batch stages.
// Buckets are 4 KiB to 64 MiB, doubling. Larger slices are not pooled.
var arrowBufferPool = pool.New(1<<12, 1<<26, 2, func(size int) any {
	return newAlignedBuffer(size)
})

// ArrowAllocator is a memory.Allocator backed by arrowBufferPool. Use it in place
// of memory.DefaultAllocator, which is a plain make that is only freed by the
// garbage collector.
//
// It lives in this package, not in pkg/chunkenc, because chunkenc imports log
// and so cannot be imported here. Chunkenc can use it for the columns it decodes.
//
// Arrow calls Free exactly once, when the reference count of a buffer drops to
// zero, so a slice goes back to the pool only when the last array using it is
// released. A buffer that is never released is not reused, but it is not leaked
// either: the garbage collector reclaims it.
//
// The memory is not zeroed. Arrow's builders zero what they grow themselves.
// Code that writes into a buffer directly must write everything it reads.
var ArrowAllocator memory.Allocator = pooledArrowAllocator{}

type pooledArrowAllocator struct{}

func (pooledArrowAllocator) Allocate(size int) []byte {
	return arrowBufferPool.Get(size).([]byte)[:size]
}

func (pooledArrowAllocator) Reallocate(size int, b []byte) []byte {
	if cap(b) >= size {
		return b[:size] // still fits into the bucket, no copy needed
	}
	newBuf := arrowBufferPool.Get(size).([]byte)[:size]
	copy(newBuf, b)
	arrowBufferPool.Put(b)
	return newBuf
}

func (pooledArrowAllocator) Free(b []byte) {
	// Put files b under the first bucket that is at least as large as cap(b), so
	// only slices that came from Allocate may be returned. Those always have the
	// exact capacity of a bucket.
	if cap(b) > 0 {
		arrowBufferPool.Put(b)
	}
}

// newAlignedBuffer mirrors the 64-byte alignment of memory.DefaultAllocator,
// whose logic is not exported. Reslicing, which is all the pool does, never
// moves the start of a slice, so the alignment survives reuse.
func newAlignedBuffer(size int) []byte {
	const alignment = 64
	buf := make([]byte, size+alignment)
	addr := uintptr(unsafe.Pointer(&buf[0]))
	shift := int((-addr) & (alignment - 1))
	return buf[shift : size+shift : size+shift]
}

var _ memory.Allocator = pooledArrowAllocator{}
