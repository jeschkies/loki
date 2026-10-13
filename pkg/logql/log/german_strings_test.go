package log

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/require"
)

// PackedHeader reads arrow.ViewHeader's memory, so these tests fail if arrow changes the layout.
func TestPackedHeader_LayoutMatchesViewHeader(t *testing.T) {
	require.Equal(t, arrow.ViewHeaderSizeBytes, PackedHeaderSizeBytes)

	for _, s := range []string{"", "a", "abc", "abcd", "abcde", "twelve bytes", "thirteen byte", strings.Repeat("long", 10)} {
		t.Run(fmt.Sprintf("len=%d", len(s)), func(t *testing.T) {
			var vh arrow.ViewHeader
			vh.SetString(s)
			if !arrow.IsViewInline(len(s)) {
				vh.SetIndexOffset(2, 77)
			}

			ph := PackedHeaderTraits.CastFromBytes(arrow.ViewHeaderTraits.CastToBytes([]arrow.ViewHeader{vh}))
			require.Len(t, ph, 1)

			require.EqualValues(t, vh.Len(), ph[0].Length())
			require.Equal(t, vh.IsInline(), ph[0].IsInline())
			prefix := vh.Prefix()
			require.Equal(t, binary.LittleEndian.Uint32(prefix[:]), ph[0].Prefix())
			if !vh.IsInline() {
				require.EqualValues(t, 2, ph[0].BufferIndex())
				require.EqualValues(t, 77, ph[0].BufferOffset())
			}
		})
	}
}

func TestPackedHeaderTraits_CastFromBytes(t *testing.T) {
	headers := make([]arrow.ViewHeader, 3)
	for i := range headers {
		headers[i].SetString(fmt.Sprintf("value-%d", i))
	}
	b := arrow.ViewHeaderTraits.CastToBytes(headers)

	t.Run("does not copy", func(t *testing.T) {
		ph := PackedHeaderTraits.CastFromBytes(b)
		require.Len(t, ph, 3)
		require.Equal(t, unsafe.Pointer(&b[0]), unsafe.Pointer(&ph[0]))
	})

	t.Run("round trip", func(t *testing.T) {
		ph := PackedHeaderTraits.CastFromBytes(b)
		require.Equal(t, b, PackedHeaderTraits.CastToBytes(ph))
	})

	t.Run("empty", func(t *testing.T) {
		require.Nil(t, PackedHeaderTraits.CastFromBytes(nil))
		require.Nil(t, PackedHeaderTraits.CastToBytes(nil))
	})

	t.Run("length is not a multiple of the header size", func(t *testing.T) {
		require.Panics(t, func() { PackedHeaderTraits.CastFromBytes(b[:PackedHeaderSizeBytes+1]) })
	})

	t.Run("misaligned", func(t *testing.T) {
		buf := make([]byte, 2*PackedHeaderSizeBytes+8)
		for i := range 8 {
			if uintptr(unsafe.Pointer(&buf[i]))%8 != 0 {
				require.Panics(t, func() { PackedHeaderTraits.CastFromBytes(buf[i : i+PackedHeaderSizeBytes]) })
				return
			}
		}
		t.Fatal("no misaligned offset found")
	})

	t.Run("bytes required", func(t *testing.T) {
		require.Equal(t, arrow.ViewHeaderTraits.BytesRequired(5), PackedHeaderTraits.BytesRequired(5))
	})
}

// packedLiteral must agree with string equality for every string an Arrow
// StringView array can hold, including long strings in later data buffers.
func TestPackedLiteral_Equals(t *testing.T) {
	long := "this string is longer than twelve bytes"

	values := []string{
		"", "a", "abc", "abcd", "abcde", "abcd-1", "abcd-2", "twelve bytes", "twelve bytez",
		"thirteen byte", "thirteen bytf", long, long[:len(long)-1] + "!", long + "!",
		strings.Repeat("x", 13) + "a", strings.Repeat("x", 13) + "b",
	}
	// Enough long strings to fill more than one 32 KB data block of the builder.
	for i := range 2000 {
		values = append(values, fmt.Sprintf("a long value that is not inline, number %06d", i))
	}

	builder := array.NewStringViewBuilder(memory.DefaultAllocator)
	defer builder.Release()
	for _, v := range values {
		builder.Append(v)
	}
	arr := builder.NewStringViewArray()
	defer arr.Release()

	headers := PackedHeaderTraits.CastFromBytes(arr.Data().Buffers()[1].Bytes())
	buffers := arr.Data().Buffers()[2:]
	require.Len(t, headers, len(values))
	require.Greater(t, len(buffers), 1, "the test must cover long strings in a later buffer")

	for _, lit := range []string{"", "abc", "abcd", "abcd-2", "twelve bytes", "thirteen byte", long, values[len(values)-1], "not in the array"} {
		t.Run(fmt.Sprintf("len=%d/%.16s", len(lit), lit), func(t *testing.T) {
			want := newPackedLiteral(lit)
			require.Equal(t, len(lit) <= 12, want.inline)

			for i := range headers {
				got := headers[i].Lo == want.lo() && want.restEquals(&headers[i], buffers)
				require.Equal(t, values[i] == lit, got, "value %q", values[i])
			}
		})
	}
}

// NewPackedHeader must build the same headers as an Arrow builder, for inline
// strings and for strings in the data buffers.
func TestNewPackedHeader_MatchesBuilder(t *testing.T) {
	var values []string
	for n := 0; n <= 40; n++ {
		values = append(values, strings.Repeat("x", n)+fmt.Sprint(n%10))
	}
	values = append(values, "", "a", "abcd", "abcde", "twelve bytes", "thirteen byte")

	builder := array.NewStringViewBuilder(memory.DefaultAllocator)
	defer builder.Release()
	for _, v := range values {
		builder.Append(v)
	}
	arr := builder.NewStringViewArray()
	defer arr.Release()
	headers := PackedHeaderTraits.CastFromBytes(arr.Data().Buffers()[1].Bytes())

	for i, v := range values {
		want := headers[i]
		got := NewPackedHeader([]byte(v), want.BufferIndex(), want.BufferOffset())
		require.Equal(t, want, got, "value %q", v)
	}
}

func TestNewStructuredMetadataMap(t *testing.T) {
	data := []byte("a-string-that-is-not-inline")
	long := NewPackedHeader(data, 0, 0)
	short := NewPackedHeader([]byte("app"), 0, 0)

	m := NewStructuredMetadataMap(
		[]int32{0, 2, 2, 3},
		[]PackedHeader{short, short, short},
		[]PackedHeader{long, short, long},
		memory.NewBufferBytes(data),
	)
	defer m.Release()

	require.Equal(t, 3, m.Len())
	offs, keys, vals := (&ArrowBatch{StructuredMetadataView: m}).StructuredMetadataColumns()
	require.Equal(t, []int32{0, 2, 2, 3}, offs[:4])
	require.Equal(t, "app", keys.Value(0))
	require.Equal(t, string(data), vals.Value(0))
	require.Equal(t, "app", vals.Value(1))
	require.Equal(t, string(data), vals.Value(2))

	require.Panics(t, func() { NewStructuredMetadataMap([]int32{0, 5}, []PackedHeader{short}, []PackedHeader{short}) })
}
