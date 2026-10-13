package chunkenc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"

	"github.com/grafana/loki/v3/pkg/compression"
	"github.com/grafana/loki/v3/pkg/logql/log"
)

// BenchmarkFilterBlockStructuredMetadata measures `| <key>="<value>"` on the structured
// metadata of one block, from the compressed bytes of the block to the surviving entries:
//
//   - row reads the row based block (chunk format V4) with the entryBufferedIterator, which
//     decompresses it, resolves every entry's symbols to labels and calls StreamPipeline.Process.
//   - columnar reads the columnar block (chunk format V5): it decompresses it, builds the
//     ArrowBatch with its structured metadata view on the symbol table and calls
//     StreamPipeline.ProcessBatch.
//
// Both start from the same entries. Writing the blocks and loading the chunk's symbols (the
// symbolizer or the symbolTable, once per chunk) are not measured. The matrix is the string
// length x block size x codec x labels per entry x percentage of matching entries. The block
// size, which is the size of its lines before compression, sets the number of entries. Strings of up to 12 bytes are inline in their header.
func BenchmarkFilterBlockStructuredMetadata(b *testing.B) {
	for _, lengths := range []struct{ name, key, value string }{
		{"inline", "app", "match"},
		{"long", "application_name_label", "matching_value_that_is_long"},
	} {
		for _, blockSize := range []int{256 * 1024, 512 * 1024, 1024 * 1024} {
			for _, enc := range []compression.Codec{compression.Snappy, compression.Zstd} {
				for _, numLabels := range []int{1, 5, 20} {
					for _, matchPct := range []int{0, 50, 100} {
						name := fmt.Sprintf("strlen=%s/block=%dKiB/codec=%s/labels=%d/matches=%d%%", lengths.name, blockSize/1024, enc, numLabels, matchPct)
						b.Run(name, func(b *testing.B) {
							benchmarkFilterBlock(b, blockSize, enc, lengths.key, lengths.value, numLabels, matchPct)
						})
					}
				}
			}
		}
	}
}

func benchmarkFilterBlock(b *testing.B, blockSize int, enc compression.Codec, key, value string, numLabels, matchPct int) {
	entries := benchmarkEntries(blockSize, false)
	numEntries := len(entries)
	var lineBytes int64
	for i := range entries {
		entries[i].metadata = benchmarkStructuredMetadata(rand.New(rand.NewSource(int64(i))), numLabels, matchPct, key, value)
		lineBytes += int64(len(entries[i].line))
	}

	// With structured metadata both formats serialise the same head block.
	head := newUnorderedHeadBlock(UnorderedWithStructuredMetadataHeadBlockFmt, newSymbolizer())
	for _, e := range entries {
		_, err := head.Append(e.ts, e.line, e.metadata)
		require.NoError(b, err)
	}
	pool := compression.GetWriterPool(enc)
	rowBlock, err := head.Serialise(pool)
	require.NoError(b, err)
	colBlock, err := head.SerialiseColumnar(pool)
	require.NoError(b, err)
	table := testSymbolTable(b, head.symbolizer, enc)
	readerPool := compression.GetReaderPool(enc)

	newPipeline := func() log.StreamPipeline {
		filter := log.NewStringLabelFilter(labels.MustNewMatcher(labels.MatchEqual, key, value))
		return log.NewPipeline([]log.Stage{filter}).ForStream(labels.EmptyLabels())
	}

	// filterRow returns the timestamps of the entries that pass.
	filterRow := func(pipeline log.StreamPipeline, timestamps []int64) []int64 {
		it := newEntryIterator(context.Background(), readerPool, rowBlock, pipeline, ChunkFormatV4, head.symbolizer)
		for it.Next() {
			timestamps = append(timestamps, it.At().Timestamp.UnixNano())
		}
		require.NoError(b, it.Err())
		require.NoError(b, it.Close())
		return timestamps
	}

	// The decompressed block is read into a reused buffer, as a pooled one would be.
	var uncompressed int
	{
		r, err := readerPool.GetReader(bytes.NewReader(colBlock))
		require.NoError(b, err)
		data, err := io.ReadAll(r)
		require.NoError(b, err)
		uncompressed = len(data)
	}
	buf := make([]byte, uncompressed)

	// filterColumnar returns the timestamps of the entries that pass.
	filterColumnar := func(pipeline log.StreamPipeline, timestamps []int64) []int64 {
		r, err := readerPool.GetReader(bytes.NewReader(colBlock))
		require.NoError(b, err)
		_, err = io.ReadFull(r, buf)
		require.NoError(b, err)
		readerPool.PutReader(r)

		batch, err := decodeColumnarBlockView(buf, numEntries, table)
		require.NoError(b, err)
		defer batch.Timestamps.Release()
		defer batch.LineColumn.Release()
		defer batch.StructuredMetadataView.Release()

		// TODO: LineFilterLabelFilter.ProcessBatch does not handle a nil Selection yet.
		batch.Selection = make([]int, numEntries)
		for i := range batch.Selection {
			batch.Selection[i] = i
		}

		for _, i := range pipeline.ProcessBatch(batch).Selection {
			timestamps = append(timestamps, batch.Timestamps.Value(i))
		}
		return timestamps
	}

	// Both paths must keep the same entries.
	want := filterRow(newPipeline(), nil)
	require.Equal(b, want, filterColumnar(newPipeline(), nil))
	b.Logf("%d of %d entries pass", len(want), numEntries)

	for _, impl := range []struct {
		name   string
		filter func(log.StreamPipeline, []int64) []int64
	}{
		{"row", filterRow},
		{"columnar", filterColumnar},
	} {
		b.Run("impl="+impl.name, func(b *testing.B) {
			pipeline := newPipeline()
			timestamps := make([]int64, 0, numEntries)

			b.ReportAllocs()
			b.SetBytes(lineBytes)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				timestamps = impl.filter(pipeline, timestamps[:0])
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(numEntries), "ns/entry")
		})
	}
}

// benchmarkStructuredMetadata returns numLabels labels, with key in the middle. It has value for
// matchPct percent of the entries, chosen with rnd. The other labels and the other values are as
// long as key and value, so they are inline only if those are, and differ from them in their first
// bytes, so a length and prefix check can reject them.
func benchmarkStructuredMetadata(rnd *rand.Rand, numLabels, matchPct int, key, value string) labels.Labels {
	long := len(key) > 12 || len(value) > 12

	// Labels are sorted by name. Names below key start with "a0", names above with "b".
	below, above, otherValue, nonMatch := "a0_%02d", "b_%02d", "v%d", "other-%d"
	if long {
		below, above = "a0_label_with_a_long_name_%02d", "label_with_a_long_name_%02d"
		otherValue, nonMatch = "value_that_is_long_%d", "other_value_that_is_long_%d"
	}

	builder := labels.NewScratchBuilder(numLabels)
	for l := 0; l < numLabels; l++ {
		switch {
		case l == numLabels/2:
			v := value
			if rnd.Intn(100) >= matchPct {
				v = fmt.Sprintf(nonMatch, rnd.Intn(10))
			}
			builder.Add(key, v)
		case l < numLabels/2:
			builder.Add(fmt.Sprintf(below, l), fmt.Sprintf(otherValue, rnd.Intn(100)))
		default:
			builder.Add(fmt.Sprintf(above, l), fmt.Sprintf(otherValue, rnd.Intn(100)))
		}
	}
	builder.Sort()
	return builder.Labels()
}
