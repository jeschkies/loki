package log

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

// PackedHeader mirrors the 16 byte layout of arrow.ViewHeader (a "German
// string" header) as two plain uint64 words. Arrow's ViewHeader hides its
// fields, and ViewHeader.Equals is too big to be inlined into a scan loop.
// Two words can be compared with plain integer comparisons instead.
//
// The layout is little endian:
//
//	Lo: bytes 0..3 hold the length (int32), bytes 4..7 hold the first 4 bytes
//	    of the string, which is the whole string if it is not longer than 4.
//	Hi: if the string is inline (length <= 12) the remaining string bytes,
//	    zero padded. Otherwise bytes 8..11 hold the buffer index and
//	    bytes 12..15 the offset into that buffer.
//
// Compare Lo first. It rejects almost all non-matching strings. For an inline
// string Lo and Hi together are the whole string.
type PackedHeader struct {
	Lo, Hi uint64
}

// packedHeaderInlineLimit is the longest string that is stored inline.
const packedHeaderInlineLimit = 12

// Length returns the length of the string.
func (h *PackedHeader) Length() int32 { return int32(h.Lo) }

// IsInline reports whether the whole string is stored in the header.
func (h *PackedHeader) IsInline() bool { return h.Length() <= packedHeaderInlineLimit }

// Prefix returns the first 4 bytes of the string.
func (h *PackedHeader) Prefix() uint32 { return uint32(h.Lo >> 32) }

// BufferIndex returns the index of the data buffer of a string that is not inline.
func (h *PackedHeader) BufferIndex() int32 { return int32(h.Hi) }

// BufferOffset returns the offset into the data buffer of a string that is not inline.
func (h *PackedHeader) BufferOffset() int32 { return int32(h.Hi >> 32) }

// PackedHeaderSizeBytes is the size of a PackedHeader. It is the same as arrow.ViewHeaderSizeBytes.
const PackedHeaderSizeBytes = int(unsafe.Sizeof(PackedHeader{}))

// PackedHeaderTraits is to PackedHeader what arrow.ViewHeaderTraits is to arrow.ViewHeader.
var PackedHeaderTraits packedHeaderTraits

type packedHeaderTraits struct{}

// BytesRequired returns the number of bytes needed to store n headers.
func (packedHeaderTraits) BytesRequired(n int) int { return PackedHeaderSizeBytes * n }

// CastFromBytes reinterprets the header buffer of an Arrow BinaryView or
// StringView array as a []PackedHeader without copying it.
//
// It panics if len(b) is not a multiple of PackedHeaderSizeBytes, if b is not
// 8 byte aligned, or on a big endian machine, because PackedHeader assumes the
// little endian layout. Arrow allocates its buffers 64 byte aligned.
func (packedHeaderTraits) CastFromBytes(b []byte) []PackedHeader {
	if len(b) == 0 {
		return nil
	}
	if len(b)%PackedHeaderSizeBytes != 0 {
		panic(fmt.Sprintf("packed header: buffer length %d is not a multiple of %d", len(b), PackedHeaderSizeBytes))
	}
	if uintptr(unsafe.Pointer(&b[0]))%unsafe.Alignof(PackedHeader{}) != 0 {
		panic("packed header: buffer is not 8 byte aligned")
	}
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		panic("packed header: big endian is not supported")
	}
	return unsafe.Slice((*PackedHeader)(unsafe.Pointer(&b[0])), len(b)/PackedHeaderSizeBytes)
}

// CastToBytes reinterprets headers as bytes without copying them.
func (packedHeaderTraits) CastToBytes(headers []PackedHeader) []byte {
	if len(headers) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&headers[0])), len(headers)*PackedHeaderSizeBytes)
}

// packedLiteral is a string to search for and the PackedHeader it would have.
type packedLiteral struct {
	lit    []byte
	hdr    PackedHeader
	inline bool
}

// newPackedLiteral returns the packedLiteral for s.
func newPackedLiteral(s string) packedLiteral {
	// Let arrow build the header, so the layouts cannot differ. For a string
	// that is not inline only the length and the prefix are set. Hi stays zero
	// and must not be compared.
	var h arrow.ViewHeader
	h.SetString(s)
	b := unsafe.Slice((*byte)(unsafe.Pointer(&h)), PackedHeaderSizeBytes)

	return packedLiteral{
		lit: []byte(s),
		hdr: PackedHeader{
			Lo: binary.LittleEndian.Uint64(b[0:8]),
			Hi: binary.LittleEndian.Uint64(b[8:16]),
		},
		inline: len(s) <= packedHeaderInlineLimit,
	}
}

// lo returns the length and prefix word to compare to a header's Lo.
func (l *packedLiteral) lo() uint64 { return l.hdr.Lo }

// restEquals reports whether h equals the literal, given that h.Lo equals lo().
// An inline literal only needs Hi. A literal that is not inline is compared
// to the bytes in the data buffer h refers to. buffers are the variadic data
// buffers of the array, which follow its validity and header buffers.
func (l *packedLiteral) restEquals(h *PackedHeader, buffers []*memory.Buffer) bool {
	if l.inline {
		return h.Hi == l.hdr.Hi
	}
	offset := h.BufferOffset()
	return bytes.Equal(buffers[h.BufferIndex()].Bytes()[offset:offset+int32(len(l.lit))], l.lit)
}
