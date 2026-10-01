package log

import (
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/prometheus/prometheus/util/pool"
)

// logArrowBufferPool pools the backing byte slices for the logfmt labels
// column (BuildLogfmtLabelsColumn's list/struct/key/value buffers). Same
// pattern as pkg/chunkenc/arrow_pool.go's arrowBufferPool - duplicated
// rather than imported, since pkg/chunkenc imports pkg/logql/log (for
// ArrowBatch), so importing the other way would be a cycle.
var logArrowBufferPool = pool.New(1<<12, 1<<23, 2, func(size int) any {
	return newAlignedArrowBuffer(size)
})

// logArrowAllocator is a memory.Allocator backed by logArrowBufferPool,
// used in place of memory.DefaultAllocator so building the labels column
// doesn't allocate brand-new buffers from scratch on every batch.
//
// Same safety argument as pkg/chunkenc/arrow_pool.go's
// pooledArrowAllocator: Buffer.Release() calls Allocator.Free() exactly
// once, only once a buffer's refcount reaches zero; array/bufferbuilder.go
// zeroes any newly grown capacity itself on every Allocate/Reallocate, so
// reused (dirty) pooled buffers are safe; and the same allocator is
// threaded uniformly through every buffer a builder owns.
type logArrowAllocator struct{}

var logArrowMem memory.Allocator = logArrowAllocator{}

func (logArrowAllocator) Allocate(size int) []byte {
	buf := logArrowBufferPool.Get(size).([]byte)
	return buf[:size]
}

func (logArrowAllocator) Reallocate(size int, b []byte) []byte {
	if cap(b) >= size {
		return b[:size]
	}
	newBuf := logArrowBufferPool.Get(size).([]byte)[:size]
	copy(newBuf, b)
	logArrowBufferPool.Put(b)
	return newBuf
}

func (logArrowAllocator) Free(b []byte) {
	logArrowBufferPool.Put(b)
}

// newAlignedArrowBuffer mirrors memory.DefaultAllocator's own 64-byte
// alignment scheme, reimplemented here since that logic isn't exported.
func newAlignedArrowBuffer(size int) []byte {
	const alignment = 64
	buf := make([]byte, size+alignment)
	addr := uintptr(unsafe.Pointer(&buf[0]))
	shift := int((-addr) & (alignment - 1))
	return buf[shift : size+shift : size+shift]
}

var _ memory.Allocator = logArrowAllocator{}
