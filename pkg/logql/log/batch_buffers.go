package log

import (
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow/memory"
)

// Data buffer indices of the columns built by batch stages. See extraBuffer.
const (
	lineBufferIndex  = 0 // the data buffer of the line column
	extraBufferIndex = 1
)

// headerList is an append-only list of headers in a buffer from ArrowAllocator.
//
// The caller owns the reference of the buffer returned by finish and has to
// release it.
type headerList struct {
	buf *memory.Buffer
	s   []PackedHeader
}

// newHeaderList returns a list with room for capacity headers.
func newHeaderList(capacity int) headerList {
	h := headerList{buf: memory.NewResizableBuffer(ArrowAllocator)}
	if capacity > 0 {
		h.reserve(capacity)
	}
	return h
}

func (h *headerList) reserve(capacity int) {
	h.buf.Reserve(capacity * PackedHeaderSizeBytes)
	h.s = PackedHeaderTraits.CastFromBytes(h.buf.Buf())[:len(h.s)]
}

func (h *headerList) append(header PackedHeader) {
	if len(h.s) == cap(h.s) {
		h.reserve(max(2*cap(h.s), 64))
	}
	h.s = append(h.s, header) // within capacity, never reallocates
}

// finish sets the length of the buffer and returns it. The list must not be used
// afterwards.
func (h *headerList) finish() *memory.Buffer {
	h.buf.ResizeNoShrink(len(h.s) * PackedHeaderSizeBytes)
	return h.buf
}

// extraBuffer collects the bytes that cannot be referenced in the line column:
// unescaped values, values with invalid UTF-8 replaced, sanitized keys and error
// details. It is the data buffer with index extraBufferIndex of the resulting
// map.
//
// The buffer is only allocated when something is written to it, so batches
// without such bytes cost nothing.
type extraBuffer struct {
	buf *memory.Buffer
	b   []byte
}

// header appends s to the buffer and returns a header referencing it.
func (e *extraBuffer) header(s []byte) PackedHeader {
	offset := len(e.b)
	e.grow(len(s))
	e.b = append(e.b, s...) // within capacity, never reallocates
	return NewPackedHeader(e.b[offset:], extraBufferIndex, int32(offset))
}

// grow makes room for n more bytes.
func (e *extraBuffer) grow(n int) {
	offset := len(e.b)
	if offset+n <= cap(e.b) {
		return
	}
	if e.buf == nil {
		e.buf = memory.NewResizableBuffer(ArrowAllocator)
	}
	e.buf.Reserve(max(2*cap(e.b), offset+n))
	// Limit the capacity to the length of the buffer. The slice of the pooled
	// bucket is longer, but Reallocate only copies up to the length, so bytes
	// written beyond it would be lost when the buffer grows again.
	buf := e.buf.Buf()
	e.b = buf[:offset:len(buf)]
}

// reserve returns the contents of the buffer with room for at least n more bytes,
// for code that appends to it without going through header, such as a decoder
// that unquotes directly into the buffer. The result must be handed back to
// commit, and nothing may be added to the buffer in between.
//
// Appending more than n bytes would make append allocate a slice of its own. The
// bytes would not be part of the buffer, and commit panics.
func (e *extraBuffer) reserve(n int) []byte {
	e.grow(n)
	return e.b
}

// commit sets the contents of the buffer to b, which is what reserve returned,
// with bytes appended to it.
func (e *extraBuffer) commit(b []byte) {
	if len(b) > cap(e.b) || (len(b) > 0 && unsafe.SliceData(b) != unsafe.SliceData(e.b)) {
		panic("extra buffer: more bytes were appended than reserved")
	}
	e.b = b
}

// finish sets the length of the buffer and returns it, or nil if nothing was
// written. The extraBuffer must not be used afterwards.
func (e *extraBuffer) finish() *memory.Buffer {
	if e.buf == nil {
		return nil
	}
	e.buf.ResizeNoShrink(len(e.b))
	return e.buf
}
