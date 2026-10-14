package log

import (
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
)

func TestArrowAllocator_Allocate(t *testing.T) {
	for _, size := range []int{1, 100, 4096, 4097, 1 << 20, 1<<26 + 1} {
		b := ArrowAllocator.Allocate(size)
		require.Len(t, b, size)
		require.GreaterOrEqual(t, cap(b), size)
		require.Zero(t, uintptr(unsafe.Pointer(&b[0]))%64, "size %d is not 64 byte aligned", size)
		ArrowAllocator.Free(b)
	}
}

// Free must only hand slices to the pool that fit their bucket. A slice with no
// capacity would otherwise be returned by a later Allocate.
func TestArrowAllocator_FreeIgnoresUnusableSlices(t *testing.T) {
	ArrowAllocator.Free(nil)
	ArrowAllocator.Free([]byte{})

	for i := 0; i < 10; i++ {
		b := ArrowAllocator.Allocate(100)
		require.Len(t, b, 100)
		ArrowAllocator.Free(b)
	}
}

func TestArrowAllocator_Reallocate(t *testing.T) {
	b := ArrowAllocator.Allocate(100)
	for i := range b {
		b[i] = byte(i)
	}

	// Still fits into the bucket: same memory.
	same := ArrowAllocator.Reallocate(200, b)
	require.Len(t, same, 200)
	require.Equal(t, unsafe.Pointer(&b[0]), unsafe.Pointer(&same[0]))

	// Does not fit: the contents move along.
	grown := ArrowAllocator.Reallocate(1<<20, same)
	require.Len(t, grown, 1<<20)
	for i := 0; i < 100; i++ {
		require.Equal(t, byte(i), grown[i])
	}
	ArrowAllocator.Free(grown)
}
