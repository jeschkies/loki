package chunkenc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/v3/pkg/compression"
	"github.com/grafana/loki/v3/pkg/logql/log"
)

type columnarTestEntry struct {
	ts       int64
	line     string
	metadata labels.Labels
}

// decompressColumnar undoes the compression of a block written by SerialiseColumnar.
func decompressColumnar(t *testing.T, enc compression.Codec, b []byte) []byte {
	t.Helper()

	r, err := compression.GetReaderPool(enc).GetReader(bytes.NewReader(b))
	require.NoError(t, err)
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	return data
}

func TestColumnarBlock_OrderedHeadBlock(t *testing.T) {
	for name, entries := range map[string][]columnarTestEntry{
		"empty": nil,
		"single entry": {
			{ts: 1_000_000_000, line: "hello"},
		},
		"equal timestamps": {
			{ts: 42, line: "a"},
			{ts: 42, line: "b"},
			{ts: 42, line: "c"},
		},
		"empty lines": {
			{ts: 1, line: ""},
			{ts: 2, line: ""},
			{ts: 3, line: "not empty"},
			{ts: 4, line: ""},
		},
		"large timestamp gaps": {
			{ts: 1 << 62, line: "first"},
			{ts: 1<<62 + 1, line: "second"},
			{ts: math.MaxInt64, line: "last"},
		},
		"multi byte lines": {
			{ts: 10, line: "héllo wörld"},
			{ts: 20, line: "日本語のログ"},
		},
	} {
		for _, enc := range testEncodings {
			t.Run(name+"/"+enc.String(), func(t *testing.T) {
				hb := &headBlock{}
				for _, e := range entries {
					_, err := hb.Append(e.ts, e.line, labels.EmptyLabels())
					require.NoError(t, err)
				}

				b, err := hb.SerialiseColumnar(compression.GetWriterPool(enc))
				require.NoError(t, err)

				data := decompressColumnar(t, enc, b)
				batch, err := decodeColumnarBlock(data, len(entries), nil)
				require.NoError(t, err)
				requireBatchMatches(t, entries, batch)
			})
		}
	}
}

func TestColumnarBlock_UnorderedHeadBlock(t *testing.T) {
	for name, tc := range map[string]struct {
		format  HeadBlockFmt
		entries []columnarTestEntry
		// expected is the order the entries come back in: by timestamp, with the write order kept
		// for equal timestamps.
		expected []columnarTestEntry
	}{
		"without structured metadata": {
			format: UnorderedHeadBlockFmt,
			entries: []columnarTestEntry{
				{ts: 30, line: "c"},
				{ts: 10, line: "a"},
				{ts: 20, line: "b"},
			},
			expected: []columnarTestEntry{
				{ts: 10, line: "a"},
				{ts: 20, line: "b"},
				{ts: 30, line: "c"},
			},
		},
		"with structured metadata": {
			format: UnorderedWithStructuredMetadataHeadBlockFmt,
			entries: []columnarTestEntry{
				{ts: 30, line: "c", metadata: labels.FromStrings("pod", "pod-2")},
				{ts: 10, line: "a", metadata: labels.FromStrings("pod", "pod-1", "trace", "abc")},
				{ts: 20, line: "b"},
			},
			expected: []columnarTestEntry{
				{ts: 10, line: "a", metadata: labels.FromStrings("pod", "pod-1", "trace", "abc")},
				{ts: 20, line: "b"},
				{ts: 30, line: "c", metadata: labels.FromStrings("pod", "pod-2")},
			},
		},
		"equal timestamps keep write order": {
			format: UnorderedWithStructuredMetadataHeadBlockFmt,
			entries: []columnarTestEntry{
				{ts: 5, line: "first", metadata: labels.FromStrings("n", "1")},
				{ts: 1, line: "zero"},
				{ts: 5, line: "second", metadata: labels.FromStrings("n", "2")},
				{ts: 5, line: "third"},
			},
			expected: []columnarTestEntry{
				{ts: 1, line: "zero"},
				{ts: 5, line: "first", metadata: labels.FromStrings("n", "1")},
				{ts: 5, line: "second", metadata: labels.FromStrings("n", "2")},
				{ts: 5, line: "third"},
			},
		},
		"empty": {
			format: UnorderedWithStructuredMetadataHeadBlockFmt,
		},
	} {
		for _, enc := range testEncodings {
			t.Run(name+"/"+enc.String(), func(t *testing.T) {
				hb := newUnorderedHeadBlock(tc.format, newSymbolizer())
				for _, e := range tc.entries {
					_, err := hb.Append(e.ts, e.line, e.metadata)
					require.NoError(t, err)
				}

				b, err := hb.SerialiseColumnar(compression.GetWriterPool(enc))
				require.NoError(t, err)

				// Blocks without structured metadata have no symbol columns to resolve.
				symbolizer := hb.symbolizer
				if tc.format < UnorderedWithStructuredMetadataHeadBlockFmt {
					symbolizer = nil
				}

				data := decompressColumnar(t, enc, b)
				batch, err := decodeColumnarBlock(data, len(tc.expected), symbolizer)
				require.NoError(t, err)
				requireBatchMatches(t, tc.expected, batch)
			})
		}
	}
}

func TestDecodeColumnarBlock_Invalid(t *testing.T) {
	hb := newUnorderedHeadBlock(UnorderedWithStructuredMetadataHeadBlockFmt, newSymbolizer())
	for i, line := range []string{"a", "bb", "ccc"} {
		_, err := hb.Append(int64(i+1), line, labels.FromStrings("pod", "p"))
		require.NoError(t, err)
	}
	enc := compression.Snappy
	b, err := hb.SerialiseColumnar(compression.GetWriterPool(enc))
	require.NoError(t, err)
	valid := decompressColumnar(t, enc, b)

	// decode gets its own copy since decoding rewrites the timestamps in place.
	decode := func(data []byte, n int) error {
		_, err := decodeColumnarBlock(bytes.Clone(data), n, hb.symbolizer)
		return err
	}

	require.NoError(t, decode(valid, 3))

	t.Run("negative entry count", func(t *testing.T) {
		require.Error(t, decode(valid, -1))
	})
	t.Run("more entries than data", func(t *testing.T) {
		require.Error(t, decode(valid, 1000))
	})
	t.Run("fewer entries than data", func(t *testing.T) {
		require.Error(t, decode(valid, 2))
	})
	t.Run("truncated timestamps", func(t *testing.T) {
		require.Error(t, decode(valid[:8], 3))
	})
	t.Run("truncated lines", func(t *testing.T) {
		// 3 entries: 24 bytes of timestamps, 16 of offsets, 6 of lines.
		require.Error(t, decode(valid[:24+16+3], 3))
	})
	t.Run("truncated symbols", func(t *testing.T) {
		require.Error(t, decode(valid[:len(valid)-1], 3))
	})
	t.Run("trailing bytes", func(t *testing.T) {
		require.Error(t, decode(append(bytes.Clone(valid), 0), 3))
	})
	t.Run("decreasing offsets", func(t *testing.T) {
		corrupt := bytes.Clone(valid)
		// Swap the offsets of lines 1 and 2, which makes them decrease.
		offsets := corrupt[24 : 24+16]
		copy(offsets[4:8], []byte{3, 0, 0, 0})
		copy(offsets[8:12], []byte{1, 0, 0, 0})
		require.Error(t, decode(corrupt, 3))
	})
	t.Run("first offset not zero", func(t *testing.T) {
		corrupt := bytes.Clone(valid)
		corrupt[24] = 1
		require.Error(t, decode(corrupt, 3))
	})
}

// requireBatchMatches checks that batch holds exactly the expected entries.
func requireBatchMatches(t *testing.T, expected []columnarTestEntry, batch *log.ArrowBatch) {
	t.Helper()

	require.Equal(t, len(expected), batch.Timestamps.Len())
	require.Equal(t, len(expected), batch.LineColumn.Len())
	require.Len(t, batch.StructuredMetadata, len(expected))
	require.Nil(t, batch.Selection, "all rows are selected")

	for i, e := range expected {
		require.Equal(t, e.ts, batch.Timestamps.Value(i), "timestamp of entry %d", i)
		require.Equal(t, e.line, batch.LineColumn.Value(i), "line of entry %d", i)

		expectedMetadata := e.metadata
		if expectedMetadata.IsEmpty() {
			expectedMetadata = labels.EmptyLabels()
		}
		require.True(t, labels.Equal(expectedMetadata, batch.StructuredMetadata[i]),
			"structured metadata of entry %d: expected %s, got %s", i, expectedMetadata, batch.StructuredMetadata[i])
	}
}

// benchmarkEntries returns log-like entries, about 150 bytes each, one millisecond apart on
// average, until their lines add up to blockSize bytes, which is when Loki cuts a block. With structuredMetadata each carries a pod and a trace id.
func benchmarkEntries(blockSize int, structuredMetadata bool) []columnarTestEntry {
	rnd := rand.New(rand.NewSource(1))
	methods := []string{"GET", "POST", "PUT", "DELETE"}
	paths := []string{"/api/v1/query", "/api/v1/push", "/loki/api/v1/labels", "/ready", "/metrics"}

	var entries []columnarTestEntry
	size := 0
	ts := int64(1_700_000_000_000_000_000)
	for size < blockSize {
		ts += int64(rnd.Intn(2_000_000))
		e := columnarTestEntry{
			ts: ts,
			line: fmt.Sprintf(`level=info ts=%d caller=handler.go:%d method=%s path=%s status=%d duration=%dms bytes=%d msg="request completed"`,
				ts, rnd.Intn(500), methods[rnd.Intn(len(methods))], paths[rnd.Intn(len(paths))],
				200+rnd.Intn(4)*100, rnd.Intn(2000), rnd.Intn(1<<20)),
		}
		if structuredMetadata {
			e.metadata = labels.FromStrings("pod", fmt.Sprintf("pod-%d", rnd.Intn(20)), "trace_id", fmt.Sprintf("%016x", rnd.Uint64()))
		}
		size += len(e.line)
		entries = append(entries, e)
	}
	return entries
}

// BenchmarkDecodeBlock compares reading all entries of a block in the row based format, with a
// bufferedIterator, to the columnar format, decompressing it and decoding it into an ArrowBatch.
// Both touch the timestamp and line of every entry. Writing the blocks is not measured.
func BenchmarkDecodeBlock(b *testing.B) {
	for _, blockSize := range []int{256 * 1024, 512 * 1024} {
		for _, structuredMetadata := range []bool{false, true} {
			benchmarkDecodeBlock(b, blockSize, structuredMetadata)
		}
	}
}

func benchmarkDecodeBlock(b *testing.B, blockSize int, structuredMetadata bool) {
	{
		entries := benchmarkEntries(blockSize, structuredMetadata)
		numEntries := len(entries)

		var (
			rowFormat byte
			rowHead   HeadBlock
			colHead   HeadBlock
			symbolzr  *symbolizer
		)
		if structuredMetadata {
			rowFormat = ChunkFormatV4
			head := newUnorderedHeadBlock(UnorderedWithStructuredMetadataHeadBlockFmt, newSymbolizer())
			rowHead, colHead, symbolzr = head, head, head.symbolizer
		} else {
			rowFormat = ChunkFormatV3
			rowHead, colHead = &headBlock{}, &headBlock{}
		}
		// With structured metadata both formats serialise the same head block, so fill it once.
		heads := []HeadBlock{rowHead}
		if colHead != rowHead {
			heads = append(heads, colHead)
		}
		for _, hb := range heads {
			for _, e := range entries {
				_, err := hb.Append(e.ts, e.line, e.metadata)
				require.NoError(b, err)
			}
			require.Equal(b, numEntries, hb.Entries())
		}

		var lineBytes int64
		for _, e := range entries {
			lineBytes += int64(len(e.line))
		}

		for _, enc := range []compression.Codec{compression.Snappy, compression.LZ4_256k, compression.LZ4_4M, compression.LZ4_Block, compression.Zstd} {
			pool := compression.GetWriterPool(enc)
			rowBlock, err := rowHead.Serialise(pool)
			require.NoError(b, err)
			colBlock, err := colHead.SerialiseColumnar(pool)
			require.NoError(b, err)

			name := fmt.Sprintf("block=%dKiB/%s/structured_metadata=%t", blockSize/1024, enc, structuredMetadata)

			b.Run(name+"/row", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(lineBytes)
				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					it := newBufferedIterator(context.Background(), compression.GetReaderPool(enc), rowBlock, rowFormat, symbolzr)
					var sum int64
					count := 0
					for it.Next() {
						sum += it.currTs + int64(len(it.currLine))
						count++
					}
					require.NoError(b, it.Err())
					require.Equal(b, numEntries, count)
					_ = sum
				}
			})

			b.Run(name+"/columnar", func(b *testing.B) {
				var uncompressed int
				{
					r, err := compression.GetReaderPool(enc).GetReader(bytes.NewReader(colBlock))
					require.NoError(b, err)
					data, err := io.ReadAll(r)
					require.NoError(b, err)
					uncompressed = len(data)
				}
				// The buffer is reused like a pooled one would be.
				buf := make([]byte, uncompressed)

				b.ReportAllocs()
				b.SetBytes(lineBytes)
				b.ResetTimer()

				for i := 0; i < b.N; i++ {
					readerPool := compression.GetReaderPool(enc)
					r, err := readerPool.GetReader(bytes.NewReader(colBlock))
					require.NoError(b, err)
					_, err = io.ReadFull(r, buf)
					require.NoError(b, err)
					readerPool.PutReader(r)

					batch, err := decodeColumnarBlock(buf, numEntries, symbolzr)
					require.NoError(b, err)

					var sum int64
					for j := 0; j < numEntries; j++ {
						sum += batch.Timestamps.Value(j) + int64(len(batch.LineColumn.Value(j)))
					}
					_ = sum
					require.Equal(b, numEntries, batch.LineColumn.Len())
					batch.Timestamps.Release()
					batch.LineColumn.Release()
				}
			})
		}
	}
}
