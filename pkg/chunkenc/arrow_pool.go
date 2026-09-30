package chunkenc

import (
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/prometheus/prometheus/util/pool"
)

// arrowBufferPool pools the backing byte slices for ArrowBatch construction
// (the values, offsets, and null-bitmap buffers a StringBuilder allocates).
// Unlike BytesBufferPool/SymbolsPool (which pool per-entry decode scratch,
// a few KB at most), these need to cover a whole compressed block's
// uncompressed size at once - chunk_block_size defaults to 256KiB, and
// larger blocks aren't unusual - hence the wider, separate size range.
var arrowBufferPool = pool.New(1<<16, 1<<23, 2, func(size int) any {
	return newAlignedBuffer(size)
})

// pooledArrowAllocator is a memory.Allocator backed by arrowBufferPool, used
// in place of memory.DefaultAllocator so decodeBlockToArrowBatch doesn't
// allocate a brand-new multi-MB buffer from scratch on every single block
// decode (memory.DefaultAllocator is a plain, unpooled make([]byte, n)
// under the hood, freed only by the Go GC).
//
// Verified safe against arrow-go's own implementation before relying on
// this (see vendor/github.com/apache/arrow-go/v18/arrow):
//   - memory/buffer.go: Buffer.Release() calls Allocator.Free() exactly
//     once, only once a buffer's reference count reaches zero - safe to
//     return to a shared pool without any risk of double-free or of
//     returning a buffer something else still references.
//   - array/data.go: Data.Release() releases every constituent buffer
//     (validity, offsets, values) of a String/Binary array, so nothing
//     backing an ArrowBatch.LineColumn is leaked past its Release() call.
//   - array/bufferbuilder.go: bufferBuilder.resize() explicitly zeroes any
//     newly grown capacity itself, on every Allocate/Reallocate call; it
//     never assumes the allocator already returns zeroed memory. Reused
//     (dirty) pooled buffers are therefore safe here.
//   - array/builder.go: the same Allocator passed to NewStringBuilder is
//     threaded through uniformly to the values, offsets, and null-bitmap
//     buffers - one allocator, no risk of a buffer using a different one
//     than the pool it gets Free()'d into.
type pooledArrowAllocator struct{}

var arrowAllocator memory.Allocator = pooledArrowAllocator{}

func (pooledArrowAllocator) Allocate(size int) []byte {
	buf := arrowBufferPool.Get(size).([]byte)
	return buf[:size]
}

func (pooledArrowAllocator) Reallocate(size int, b []byte) []byte {
	if cap(b) >= size {
		return b[:size]
	}
	newBuf := arrowBufferPool.Get(size).([]byte)[:size]
	copy(newBuf, b)
	arrowBufferPool.Put(b)
	return newBuf
}

func (pooledArrowAllocator) Free(b []byte) {
	arrowBufferPool.Put(b)
}

// newAlignedBuffer mirrors memory.DefaultAllocator's own 64-byte alignment
// scheme (vendor arrow/memory/go_allocator.go, util.go), reimplemented here
// since that logic isn't exported. Reslicing an aligned slice - which is
// all arrowBufferPool's Get/Put(Put reslices to [0:0], Get reslices back
// up) ever does - never moves its start address, so alignment survives
// reuse through the pool without needing to be recomputed each time.
func newAlignedBuffer(size int) []byte {
	const alignment = 64
	buf := make([]byte, size+alignment)
	addr := uintptr(unsafe.Pointer(&buf[0]))
	shift := int((-addr) & (alignment - 1))
	return buf[shift : size+shift : size+shift]
}

var _ memory.Allocator = pooledArrowAllocator{}
