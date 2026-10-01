package log

import (
	"encoding/binary"
	"unsafe"
)

// PackedHeader mirrors arrow.ViewHeader's exact 16-byte bit layout (a
// Umbra/German-string header: length+prefix in the first 8 bytes, then
// either the rest of an inline value or a buffer index+offset in the
// second 8 bytes) as two plain uint64 fields.
//
// This exists because arrow.ViewHeader.Equals already implements the
// right algorithm (compare length+prefix first, only do a deep compare
// for survivors) but can't be inlined into a per-row scan loop (it's a
// cross-package method call with internal branching - confirmed via
// generated assembly), and array.StringView exposes no bulk accessor for
// its underlying header array at all (only ValueHeader(i), one at a
// time). PackedHeader gives us a real []PackedHeader we can scan
// directly - prerequisite for comparing many rows per instruction later.
//
// Layout (little-endian, matching arrow.ViewHeader and this platform):
//
//	Lo: bytes[0:4] = length (int32), bytes[4:8] = first 4 bytes of the
//	    string (prefix if out-of-line, or part of the inline data)
//	Hi: if length <= 12 (inline), bytes[8:16] = remaining inline data
//	    if length > 12 (out-of-line), bytes[8:12] = buffer index,
//	    bytes[12:16] = offset into that buffer
type PackedHeader struct {
	Lo, Hi uint64
}

const packedHeaderInlineLimit = 12

// Length returns the string's length.
func (h *PackedHeader) Length() int32 { return int32(h.Lo) }

// IsInline reports whether the full value is stored inline in the
// header (length <= 12), vs out-of-line in a shared buffer.
func (h *PackedHeader) IsInline() bool { return h.Length() <= packedHeaderInlineLimit }

// Prefix returns the first 4 bytes of the string, valid whether the
// value is inline or not.
func (h *PackedHeader) Prefix() uint32 { return uint32(h.Lo >> 32) }

// BufferIndex returns the out-of-line buffer index. Only meaningful
// when !IsInline().
func (h *PackedHeader) BufferIndex() int32 { return int32(h.Hi) }

// BufferOffset returns the byte offset into that buffer. Only
// meaningful when !IsInline().
func (h *PackedHeader) BufferOffset() int32 { return int32(h.Hi >> 32) }

// InlineBytes returns the inline string bytes as a zero-copy slice into
// h's own memory. Only valid when IsInline() is true - the caller must
// check first, since this blindly slices bytes[4:4+Length()] regardless
// of what they actually contain otherwise.
//
// Pointer receiver is required: a value receiver would copy the struct,
// and the returned slice would point into that temporary instead of the
// original header array.
func (h *PackedHeader) InlineBytes() []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(h)), 16)[4 : 4+h.Length()]
}

// setInline sets h to the inline representation of data (len(data) must
// be <= 12), zero-padding any unused tail bytes.
func (h *PackedHeader) setInline(data []byte) {
	var buf [12]byte
	copy(buf[:], data)
	h.Lo = uint64(uint32(len(data))) | uint64(binary.LittleEndian.Uint32(buf[0:4]))<<32
	h.Hi = binary.LittleEndian.Uint64(buf[4:12])
}

// setOutOfLine sets h to reference a value of the given length stored
// at bufferIndex/offset in some external buffer, with the given prefix
// (data[0:4], valid since length > 12 here).
func (h *PackedHeader) setOutOfLine(length int32, prefix [4]byte, bufferIndex, offset int32) {
	h.Lo = uint64(uint32(length)) | uint64(binary.LittleEndian.Uint32(prefix[:]))<<32
	h.Hi = uint64(uint32(bufferIndex)) | uint64(uint32(offset))<<32
}

// packedHeaderForCompare builds a synthetic PackedHeader for lit, the
// way lit would be encoded if it were itself a header - used as the
// comparison target in FilterPackedStringView. If len(lit) <= 12, this
// is a complete header (Hi holds real data, Lo+Hi together are a full
// equality check with no fallback ever needed). If len(lit) > 12, only
// Lo (length+prefix) is meaningful; Hi is left zero and must not be
// compared against a row's Hi, since a row's Hi encodes a buffer
// location, not content.
func packedHeaderForCompare(lit []byte) PackedHeader {
	var h PackedHeader
	if len(lit) <= packedHeaderInlineLimit {
		h.setInline(lit)
		return h
	}
	var prefix [4]byte
	copy(prefix[:], lit[:4])
	h.Lo = uint64(uint32(len(lit))) | uint64(binary.LittleEndian.Uint32(prefix[:]))<<32
	return h
}
