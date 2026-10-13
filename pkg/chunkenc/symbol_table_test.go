package chunkenc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/v3/pkg/compression"
	"github.com/grafana/loki/v3/pkg/logql/log"
)

// testSymbolTable serialises s and loads it back as a symbolTable, the way a chunk's symbols are read.
func testSymbolTable(t testing.TB, s *symbolizer, enc compression.Codec) *symbolTable {
	t.Helper()

	var buf bytes.Buffer
	_, _, err := s.SerializeTo(&buf, compression.GetWriterPool(enc))
	require.NoError(t, err)

	table, err := symbolTableFromEnc(buf.Bytes(), compression.GetReaderPool(enc))
	require.NoError(t, err)
	return table
}

// requireViewMatches checks that the rows of the batch's structured metadata view hold the same
// names and values, in the same order, as the labels.
func requireViewMatches(t *testing.T, expected []labels.Labels, batch *log.ArrowBatch) {
	t.Helper()

	require.Equal(t, len(expected), batch.StructuredMetadataView.Len())
	offs, keys, vals := batch.StructuredMetadataColumns()
	for i, lbls := range expected {
		var want []string
		lbls.Range(func(l labels.Label) { want = append(want, l.Name, l.Value) })

		var got []string
		for o := offs[i]; o < offs[i+1]; o++ {
			got = append(got, keys.Value(int(o)), vals.Value(int(o)))
		}
		require.Equal(t, want, got, "structured metadata of entry %d", i)
	}
}

func TestSymbolTable_Headers(t *testing.T) {
	s := newSymbolizer()
	long := strings.Repeat("long value ", 5)
	_, err := s.Add(labels.FromStrings("pod", "pod-1", "trace_id", long, "empty", ""))
	require.NoError(t, err)

	for _, enc := range testEncodings {
		t.Run(enc.String(), func(t *testing.T) {
			table := testSymbolTable(t, s, enc)

			require.Len(t, table.headers, len(s.labels))
			require.Len(t, table.needsNormalizing, len(s.labels))
			for i, want := range s.labels {
				h := table.headers[i]
				require.EqualValues(t, len(want), h.Length(), "symbol %d", i)
				if !h.IsInline() {
					off := h.BufferOffset()
					require.Equal(t, want, string(table.data.Bytes()[off:off+h.Length()]), "symbol %d", i)
				}
			}
		})
	}
}

func TestIsPlainLabelName(t *testing.T) {
	for name, plain := range map[string]bool{
		"pod": true, "trace_id": true, "Pod2": true, "a": true,
		"": false, "_a": false, "1a": false, "a__b": false, "a_": false, "a-b": false, "a.b": false, "é": false,
	} {
		require.Equal(t, plain, isPlainLabelName([]byte(name)), name)
	}
}

func TestDecodeColumnarBlockView(t *testing.T) {
	long := strings.Repeat("long value ", 5)
	entries := []columnarTestEntry{
		{ts: 1, line: "a", metadata: labels.FromStrings("pod", "pod-1", "trace_id", long)},
		{ts: 2, line: "b"},
		{ts: 3, line: "c", metadata: labels.FromStrings("pod", "pod-2", "empty", "")},
		// Names LabelNamer changes, which take the slow path, and one it does not.
		{ts: 4, line: "d", metadata: labels.FromStrings("service.name", "svc", "1st", long, "k8s.pod.name", "p")},
		{ts: 5, line: "e", metadata: labels.FromStrings("service.name", "other", "__reserved__", "x")},
		{ts: 6, line: "f", metadata: labels.FromStrings("a.very.long.attribute.name", long)},
	}

	for _, enc := range testEncodings {
		t.Run(enc.String(), func(t *testing.T) {
			hb := newUnorderedHeadBlock(UnorderedWithStructuredMetadataHeadBlockFmt, newSymbolizer())
			for _, e := range entries {
				_, err := hb.Append(e.ts, e.line, e.metadata)
				require.NoError(t, err)
			}
			b, err := hb.SerialiseColumnar(compression.GetWriterPool(enc))
			require.NoError(t, err)
			data := decompressColumnar(t, enc, b)

			// The labels are what symbolizer.Lookup resolves, including normalised names.
			rows, err := decodeColumnarBlock(bytes.Clone(data), len(entries), hb.symbolizer)
			require.NoError(t, err)

			table := testSymbolTable(t, hb.symbolizer, enc)
			batch, err := decodeColumnarBlockView(bytes.Clone(data), len(entries), table)
			require.NoError(t, err)

			require.Equal(t, len(entries), batch.Timestamps.Len())
			require.Nil(t, batch.Selection)
			for i, e := range entries {
				require.Equal(t, e.ts, batch.Timestamps.Value(i))
				require.Equal(t, e.line, batch.LineColumn.Value(i))
			}
			requireViewMatches(t, rows.StructuredMetadata, batch)
		})
	}
}

func TestDecodeColumnarBlockView_NoTable(t *testing.T) {
	hb := newUnorderedHeadBlock(UnorderedHeadBlockFmt, newSymbolizer())
	for i := range 3 {
		_, err := hb.Append(int64(i+1), fmt.Sprint(i), labels.EmptyLabels())
		require.NoError(t, err)
	}
	b, err := hb.SerialiseColumnar(compression.GetWriterPool(compression.Snappy))
	require.NoError(t, err)

	batch, err := decodeColumnarBlockView(decompressColumnar(t, compression.Snappy, b), 3, nil)
	require.NoError(t, err)
	requireViewMatches(t, []labels.Labels{labels.EmptyLabels(), labels.EmptyLabels(), labels.EmptyLabels()}, batch)
}

func TestDecodeColumnarBlockView_Invalid(t *testing.T) {
	hb := newUnorderedHeadBlock(UnorderedWithStructuredMetadataHeadBlockFmt, newSymbolizer())
	for i, line := range []string{"a", "bb", "ccc"} {
		_, err := hb.Append(int64(i+1), line, labels.FromStrings("pod", "p"))
		require.NoError(t, err)
	}
	enc := compression.Snappy
	b, err := hb.SerialiseColumnar(compression.GetWriterPool(enc))
	require.NoError(t, err)
	valid := decompressColumnar(t, enc, b)
	table := testSymbolTable(t, hb.symbolizer, enc)

	decode := func(data []byte, table *symbolTable) error {
		_, err := decodeColumnarBlockView(bytes.Clone(data), 3, table)
		return err
	}
	require.NoError(t, decode(valid, table))

	// 3 entries: 24 bytes of timestamps, 16 of offsets, 6 of lines, 12 of counts, then 3 pairs.
	symbolsAt := 24 + 16 + 6 + 12

	t.Run("symbol out of range", func(t *testing.T) {
		corrupt := bytes.Clone(valid)
		binary.LittleEndian.PutUint32(corrupt[symbolsAt+4:], uint32(len(table.headers)))
		require.Error(t, decode(corrupt, table))
	})
	t.Run("counts exceed the symbols", func(t *testing.T) {
		corrupt := bytes.Clone(valid)
		binary.LittleEndian.PutUint32(corrupt[24+16+6:], 100)
		require.Error(t, decode(corrupt, table))
	})
	t.Run("counts leave symbols over", func(t *testing.T) {
		corrupt := bytes.Clone(valid)
		binary.LittleEndian.PutUint32(corrupt[24+16+6:], 0)
		require.Error(t, decode(corrupt, table))
	})
	t.Run("truncated symbols", func(t *testing.T) {
		require.Error(t, decode(valid[:len(valid)-1], table))
	})
}

func TestSymbolTableFromEnc_Invalid(t *testing.T) {
	s := newSymbolizer()
	_, err := s.Add(labels.FromStrings("pod", "pod-1"))
	require.NoError(t, err)

	t.Run("symbol count larger than the symbols", func(t *testing.T) {
		_, err := newSymbolTable([]byte{3, 'p', 'o', 'd'}, 2)
		require.Error(t, err)
	})
	t.Run("trailing bytes", func(t *testing.T) {
		_, err := newSymbolTable([]byte{3, 'p', 'o', 'd', 0}, 1)
		require.Error(t, err)
	})
	t.Run("length past the end", func(t *testing.T) {
		_, err := newSymbolTable([]byte{9, 'p', 'o', 'd'}, 1)
		require.Error(t, err)
	})
}
