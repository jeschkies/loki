package log

import (
	"bytes"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/grafana/loki/v3/pkg/arrowfilter"
)

// PackedStringView is our own zero-copy string-view column, built
// directly over a []PackedHeader instead of going through
// array.StringView - see the design discussion this is based on: Arrow's
// StringView already implements the right comparison algorithm
// (ViewHeader.Equals), but exposes no bulk accessor for its header array
// (only ValueHeader(i), one at a time), so there's no way to scan many
// headers in a tight, inlinable loop using it directly. PackedStringView
// exists purely to make that scan possible; FilterPackedStringView is
// the two-tier scan this unlocks (tier 1: cheap Lo-only compare across
// all rows; tier 2: Hi compare or out-of-line verify, only for
// survivors) - the structure a future SIMD batch compare would plug into.
type PackedStringView struct {
	headers   []PackedHeader
	los       []uint64 // headers[i].Lo, kept contiguous for matchLo's batch scan
	headerBuf *memory.Buffer
	validity  []byte // null bitmap, 1 bit per row, LSB-first; nil means "all valid"
	buffer    *memory.Buffer
}

// Len returns the number of rows.
func (v *PackedStringView) Len() int { return len(v.headers) }

// IsNull reports whether row i has no value (e.g. the logfmt key was
// absent from that row's line).
func (v *PackedStringView) IsNull(i int) bool {
	if v.validity == nil {
		return false
	}
	return v.validity[i/8]&(1<<(i%8)) == 0
}

// Header returns a pointer to row i's header, for direct tier-1/tier-2
// scanning (see FilterPackedStringView).
func (v *PackedStringView) Header(i int) *PackedHeader { return &v.headers[i] }

// Value returns row i's value as a zero-copy slice - from the header
// itself if inline, or from the shared out-of-line buffer otherwise.
func (v *PackedStringView) Value(i int) []byte {
	h := &v.headers[i]
	if h.IsInline() {
		return h.InlineBytes()
	}
	offset := h.BufferOffset()
	length := h.Length()
	return v.buffer.Bytes()[offset : offset+length]
}

// Release releases v's buffers. v must not be used after calling this.
func (v *PackedStringView) Release() {
	if v.headerBuf != nil {
		v.headerBuf.Release()
		v.headerBuf = nil
	}
	if v.buffer != nil {
		v.buffer.Release()
		v.buffer = nil
	}
	v.headers = nil
}

// BuildPackedStringView builds a PackedStringView holding, for each
// row in lines (or, if selection is non-nil, for each row named by
// selection, in order), the raw byte span of key's value as it
// appears in that row's line (see findLogfmtValueSpan) - or null if
// key isn't present. Values up to 12 bytes are stored fully inline in
// the header; longer values reference lines' own underlying buffer
// directly via SetIndexOffset-equivalent packing, retained through
// normal buffer refcounting. Mirrors BuildLogfmtValueView exactly,
// just writing PackedHeaders instead of arrow.ViewHeaders.
//
// selection restricts which rows get scanned at all: findLogfmtValueSpan
// is a plain per-row scan, not SIMD, so when an earlier batch-capable
// stage (e.g. a line filter) has already narrowed the set of rows
// worth considering, passing its selection here means this function
// only does logfmt-scan work for rows that survived it, instead of
// scanning every row in the block and relying on a selection
// intersection afterward. The resulting view is dense and selection-
// sized (Len() == len(selection)): row i of the view corresponds to
// absolute row selection[i], and FilterPackedStringView(SIMD)'s output
// is therefore an index into selection, not into lines directly - the
// caller maps back through selection to recover the original row (see
// runFullPipelineBenchmark's PackedStringView-SIMD-Full variant). A
// nil selection means "all rows", matching ArrowBatch.Selection's own
// convention.
func BuildPackedStringView(mem memory.Allocator, lines *array.String, key []byte, selection []int32) *PackedStringView {
	m := lines.Len()
	if selection != nil {
		m = len(selection)
	}
	if m == 0 {
		return &PackedStringView{}
	}

	headerBuf := memory.NewResizableBuffer(mem)
	headerBuf.Resize(m * 16)
	headers := unsafe.Slice((*PackedHeader)(unsafe.Pointer(&headerBuf.Bytes()[0])), m)
	los := make([]uint64, m)

	validity := make([]byte, (m+7)/8)

	lineValues := lines.Data().Buffers()[2]
	offsets := lines.ValueOffsets()

	for i := 0; i < m; i++ {
		row := i
		if selection != nil {
			row = int(selection[i])
		}
		line := unsafeGetBytes(lines.Value(row))
		start, end, ok := findLogfmtValueSpan(line, key)
		if !ok {
			continue
		}
		validity[i/8] |= 1 << (i % 8)

		length := end - start
		h := &headers[i]
		if length <= packedHeaderInlineLimit {
			h.setInline(line[start:end])
		} else {
			var prefix [4]byte
			copy(prefix[:], line[start:start+4])
			h.setOutOfLine(int32(length), prefix, 0, offsets[row]+int32(start))
		}
		los[i] = h.Lo
	}

	if lineValues != nil {
		lineValues.Retain()
	}

	return &PackedStringView{
		headers:   headers,
		los:       los,
		headerBuf: headerBuf,
		validity:  validity,
		buffer:    lineValues,
	}
}

// FilterPackedStringView returns the row indices in v whose value
// equals wantEscaped (the pre-escaped comparison form from
// escapeLogfmtCompareValue - see logfmt_value_view.go), compared as-is
// against v's raw (never unescaped) bytes.
//
// Two-tier scan, matching the algorithm arrow.ViewHeader.Equals already
// uses internally, restructured so it's a real inlined loop over
// []PackedHeader instead of a per-row call:
//
//  1. Compare target.Lo (length+prefix) against every row's Lo. If
//     wantEscaped is itself <= 12 bytes, this alone is a *complete*
//     equality check for any row it matches (the entire value is
//     packed into Lo+Hi for an inline row, and a length match there
//     means the row must be inline too) once Hi is also checked.
//  2. Only for rows where Lo matched: if wantEscaped is inline, compare
//     Hi too (still header-only, no buffer touch). If wantEscaped is
//     longer than 12 bytes, Hi can't be compared directly (a row's Hi
//     encodes a buffer location, not content), so fall back to a real
//     byte comparison against the out-of-line buffer.
func FilterPackedStringView(v *PackedStringView, wantEscaped []byte) []int32 {
	target := packedHeaderForCompare(wantEscaped)
	targetInline := len(wantEscaped) <= packedHeaderInlineLimit

	rows := arrowfilter.GetSelection(v.Len())
	for i := range v.headers {
		if v.IsNull(i) {
			continue
		}
		h := &v.headers[i]
		if h.Lo != target.Lo {
			continue
		}
		if targetInline {
			if h.Hi == target.Hi {
				rows = append(rows, int32(i))
			}
			continue
		}
		if bytes.Equal(v.Value(i), wantEscaped) {
			rows = append(rows, int32(i))
		}
	}
	return rows
}

// FilterPackedStringViewSIMD is FilterPackedStringView with tier 1
// (the length+prefix compare) batched through matchLo instead of an
// inlined per-row comparison - AVX2 when available (see
// matchlo_amd64.go/.s), pure Go otherwise. Tier 2 (Hi compare or
// out-of-line verify) is unchanged: it only runs for rows matchLo
// flagged as candidates.
func FilterPackedStringViewSIMD(v *PackedStringView, wantEscaped []byte) []int32 {
	target := packedHeaderForCompare(wantEscaped)
	targetInline := len(wantEscaped) <= packedHeaderInlineLimit

	n := v.Len()
	mask := make([]byte, (n+7)/8)
	matchLo(v.los, target.Lo, mask)

	rows := arrowfilter.GetSelection(n)
	for i := 0; i < n; i++ {
		if mask[i/8]&(1<<(i%8)) == 0 {
			continue
		}
		if v.IsNull(i) {
			continue
		}
		if targetInline {
			if v.headers[i].Hi == target.Hi {
				rows = append(rows, int32(i))
			}
			continue
		}
		if bytes.Equal(v.Value(i), wantEscaped) {
			rows = append(rows, int32(i))
		}
	}
	return rows
}

// MaterializePackedValueForRow builds the final LabelsResult for row of
// v under label name key - mirroring MaterializeLogfmtValueForRow's
// contract (pkg/logql/log/logfmt_value_view.go): only ever meant to be
// called for rows that survived a filter.
func MaterializePackedValueForRow(v *PackedStringView, key string, row int, lbs *LabelsBuilder) LabelsResult {
	lbs.Reset()
	lbs.Set(ParsedLabel, key, unsafeGetString(v.Value(row)))
	return lbs.LabelsResult()
}
