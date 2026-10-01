package chunkenc

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/v3/pkg/compression"
	"github.com/grafana/loki/v3/pkg/logproto"
	"github.com/grafana/loki/v3/pkg/logql/log"
)

func TestChunkFormatV5_RoundTrip(t *testing.T) {
	for _, enc := range []compression.Codec{compression.None, compression.Snappy, compression.LZ4_256k, compression.Zstd} {
		t.Run(enc.String(), func(t *testing.T) {
			c := NewMemChunk(ChunkFormatV5, enc, UnorderedWithColumnarHeadBlockFmt, 64*1024, 1500*1024)

			type want struct {
				ts      int64
				line    string
				hasMeta bool
				traceID string
			}
			var wants []want

			r := rand.New(rand.NewSource(7))
			for i := int64(0); i < 20000; i++ {
				line := fmt.Sprintf("logline=%d level=info duration=%dms", i, r.Intn(1000))
				var meta []logproto.LabelAdapter
				hasMeta := i%3 == 0
				traceID := fmt.Sprintf("t%d", i)
				if hasMeta {
					meta = []logproto.LabelAdapter{
						{Name: "trace_id", Value: traceID},
						{Name: "pod", Value: "pod-a"},
					}
				}
				entry := &logproto.Entry{Timestamp: time.Unix(0, i), Line: line, StructuredMetadata: meta}
				require.True(t, c.SpaceFor(entry))
				_, err := c.Append(entry)
				require.NoError(t, err)

				wants = append(wants, want{ts: i, line: line, hasMeta: hasMeta, traceID: traceID})
			}
			require.NoError(t, c.Close())
			require.Greater(t, len(c.blocks), 1, "expected multiple blocks to genuinely exercise multi-block decode")

			it, err := c.Iterator(context.Background(), time.Unix(0, 0), time.Unix(0, math.MaxInt64), logproto.FORWARD, log.NewNoopPipeline().ForStream(labels.FromStrings("app", "test")))
			require.NoError(t, err)
			defer it.Close()

			var i int
			for it.Next() {
				e := it.At()
				w := wants[i]
				require.Equal(t, w.ts, e.Timestamp.UnixNano(), "entry %d timestamp", i)
				require.Equal(t, w.line, e.Line, "entry %d line", i)
				if w.hasMeta {
					require.Contains(t, it.Labels(), "trace_id=\""+w.traceID+"\"", "entry %d structured metadata", i)
					require.Contains(t, it.Labels(), "pod=\"pod-a\"", "entry %d structured metadata", i)
				}
				i++
			}
			require.NoError(t, it.Err())
			require.Len(t, wants, i)
		})
	}
}

func TestChunkFormatV5_SampleIterator(t *testing.T) {
	c := NewMemChunk(ChunkFormatV5, compression.LZ4_256k, UnorderedWithColumnarHeadBlockFmt, 256*1024, 1500*1024)

	var totalBytes int64
	for i := int64(0); i < 2000; i++ {
		line := fmt.Sprintf("line-%d-of-some-length", i)
		entry := &logproto.Entry{Timestamp: time.Unix(0, i), Line: line}
		require.True(t, c.SpaceFor(entry))
		_, err := c.Append(entry)
		require.NoError(t, err)
		totalBytes += int64(len(line))
	}
	require.NoError(t, c.Close())

	ex, err := log.NewLineSampleExtractor(log.BytesExtractor, nil, nil, false, false)
	require.NoError(t, err)
	streamEx := ex.ForStream(labels.FromStrings("app", "test"))

	sit := c.SampleIterator(context.Background(), time.Unix(0, 0), time.Unix(0, math.MaxInt64), streamEx)
	defer sit.Close()

	var gotBytes int64
	var n int
	for sit.Next() {
		gotBytes += int64(sit.At().Value)
		n++
	}
	require.NoError(t, sit.Err())
	require.Equal(t, 2000, n)
	require.Equal(t, totalBytes, gotBytes)
}

func TestChunkFormatV5_EmptyLine(t *testing.T) {
	// A lone empty-line entry with no structured metadata is a pre-existing
	// edge case: unorderedHeadBlock.IsEmpty() checks hb.size==0, so such a
	// chunk is treated as empty regardless of format (verified this isn't
	// something the new format introduced - V4 has the same behavior).
	// Mix an empty line in among non-empty ones instead, which is the
	// realistic case and genuinely exercises zero-length offsets/lines.
	c := NewMemChunk(ChunkFormatV5, compression.Snappy, UnorderedWithColumnarHeadBlockFmt, 256*1024, 1500*1024)
	lines := []string{"first line", "", "third line", "", ""}
	for i, l := range lines {
		entry := &logproto.Entry{Timestamp: time.Unix(0, int64(i)), Line: l}
		require.True(t, c.SpaceFor(entry))
		_, err := c.Append(entry)
		require.NoError(t, err)
	}
	require.NoError(t, c.Close())

	it, err := c.Iterator(context.Background(), time.Unix(0, 0), time.Unix(0, math.MaxInt64), logproto.FORWARD, log.NewNoopPipeline().ForStream(labels.FromStrings("app", "test")))
	require.NoError(t, err)
	defer it.Close()

	var got []string
	for it.Next() {
		got = append(got, it.At().Line)
	}
	require.NoError(t, it.Err())
	require.Equal(t, lines, got)
}

func TestChunkFormatV5_NoopPipelineViaEncBlock(t *testing.T) {
	// Exercises the real dispatch path (encBlock.Iterator -> newBatchEntryIterator
	// -> decodeBlockBytesToArrowBatch -> decodeColumnarBlockToArrowBatch),
	// not the bypassed drain* helpers batch_bench_test.go uses.
	c := NewMemChunk(ChunkFormatV5, compression.LZ4_256k, UnorderedWithColumnarHeadBlockFmt, 256*1024, 1500*1024)
	for i := int64(0); i < 500; i++ {
		entry := &logproto.Entry{Timestamp: time.Unix(0, i), Line: fmt.Sprintf("line %d", i)}
		require.True(t, c.SpaceFor(entry))
		_, err := c.Append(entry)
		require.NoError(t, err)
	}
	require.NoError(t, c.Close())

	pipeline := log.NewNoopPipeline().ForStream(labels.FromStrings("app", "test"))
	it, err := c.Iterator(context.Background(), time.Unix(0, 0), time.Unix(0, math.MaxInt64), logproto.FORWARD, pipeline)
	require.NoError(t, err)
	defer it.Close()

	n := 0
	for it.Next() {
		require.Equal(t, fmt.Sprintf("line %d", n), it.At().Line)
		n++
	}
	require.NoError(t, it.Err())
	require.Equal(t, 500, n)
}

func TestChunkFormatV5_FilterStage(t *testing.T) {
	// Exercises ProcessBatch (SIMD/stdlib scan) on top of the new format's
	// decode, same as the real query path would.
	c := NewMemChunk(ChunkFormatV5, compression.LZ4_256k, UnorderedWithColumnarHeadBlockFmt, 256*1024, 1500*1024)
	for i := int64(0); i < 3000; i++ {
		field := ""
		if i%7 == 0 {
			field = "matchme"
		}
		line := fmt.Sprintf("logline=%d extra=%s", i, field)
		entry := &logproto.Entry{Timestamp: time.Unix(0, i), Line: line}
		require.True(t, c.SpaceFor(entry))
		_, err := c.Append(entry)
		require.NoError(t, err)
	}
	require.NoError(t, c.Close())

	f, err := log.NewFilter("matchme", log.LineMatchEqual)
	require.NoError(t, err)
	pipeline := log.NewPipeline([]log.Stage{f.ToStage()}).ForStream(labels.FromStrings("app", "test"))

	it, err := c.Iterator(context.Background(), time.Unix(0, 0), time.Unix(0, math.MaxInt64), logproto.FORWARD, pipeline)
	require.NoError(t, err)
	defer it.Close()

	n := 0
	for it.Next() {
		require.True(t, strings.Contains(it.At().Line, "matchme"))
		n++
	}
	require.NoError(t, it.Err())
	require.Equal(t, 3000/7+1, n)
}

// TestChunkFormatV5_DecodeTruncatedBlock exercises
// decodeColumnarBlockToArrowBatch's error paths directly: a block
// truncated at any byte offset must return an error, never panic - in
// particular, it must not double-release any of timestampsBuf/
// valuesBuf/offsetsBuf through the deferred cleanup that returns them
// to arrowBufferPool on an early return (see the comment on that defer
// in columnar.go). Truncating at every offset sweeps through failing
// at each of readUvarint's several call sites in turn, after a
// different subset of those buffers has already been allocated.
func TestChunkFormatV5_DecodeTruncatedBlock(t *testing.T) {
	timestamps := make([]int64, 50)
	lines := make([]string, 50)
	symbolsPerEntry := make([]symbols, 50)
	sym := newSymbolizer()
	for i := range timestamps {
		timestamps[i] = int64(i)
		lines[i] = fmt.Sprintf("logline=%d level=info duration=%dms", i, i*7%1000)
		syms, err := sym.Add(labels.FromStrings("trace_id", fmt.Sprintf("t%d", i)))
		require.NoError(t, err)
		symbolsPerEntry[i] = syms
	}

	full, err := encodeColumnarBlock(timestamps, lines, symbolsPerEntry)
	require.NoError(t, err)
	require.NotEmpty(t, full)

	for n := 0; n < len(full); n++ {
		// full[:n] alone would only shorten len, not cap - leaving
		// enough headroom in the shared backing array that a 2-index
		// slice past n (but still within cap(full)) wouldn't bounds-
		// check the way it would against genuinely short/truncated
		// data read from disk. append([]byte(nil), ...) isn't enough
		// either: growslice can round the new capacity up past n.
		// make+copy is the only way to guarantee cap == len == n, so
		// every slice operation inside decode sees exactly n bytes
		// available, matching a real truncated read.
		truncated := make([]byte, n)
		copy(truncated, full[:n])
		require.NotPanics(t, func() {
			_, err := decodeColumnarBlockToArrowBatch(context.Background(), truncated, len(timestamps), sym)
			require.Error(t, err, "truncated to %d/%d bytes", n, len(full))
		}, "truncated to %d/%d bytes", n, len(full))
	}

	// The untruncated block must still decode cleanly - sweeping
	// truncation points shouldn't leave arrowBufferPool (shared,
	// package-level state) in a state that corrupts a subsequent,
	// valid decode.
	batch, err := decodeColumnarBlockToArrowBatch(context.Background(), full, len(timestamps), sym)
	require.NoError(t, err)
	require.Equal(t, len(timestamps), batch.NumRows())
	batch.Release()
}
