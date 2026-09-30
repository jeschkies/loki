package chunkenc

import (
	"context"
	"time"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/prometheus/prometheus/model/labels"

	"github.com/grafana/loki/v3/pkg/compression"
	"github.com/grafana/loki/v3/pkg/iter"
	"github.com/grafana/loki/v3/pkg/logproto"
	"github.com/grafana/loki/v3/pkg/logql/log"
	"github.com/grafana/loki/v3/pkg/logqlmodel/stats"
	"github.com/grafana/loki/v3/pkg/util"
)

// decodeBlockToArrowBatch decodes an entire compressed block into a
// log.ArrowBatch. It reuses bufferedIterator's existing per-entry wire
// decode (moveNext) rather than reimplementing it - the on-disk layout is
// row-major (timestamp, line, structured metadata symbols interleaved), so
// building a columnar batch is a real copy pass, not a zero-copy
// reinterpretation. The only thing that changes versus
// entryBufferedIterator/sampleBufferedIterator is *when* the
// StreamPipeline/StreamSampleExtractor runs: once for the whole block via
// ProcessBatch, instead of once per line via Process.
//
// numEntries and uncompressedSize are the block's own header fields
// (block.numEntries/block.uncompressedSize - known without decompressing
// anything), used to presize the builder and slices once instead of letting
// them grow by repeated doubling. uncompressedSize covers more than just
// line bytes (timestamps and structured metadata symbols are interleaved
// too), so it's an overestimate of the values buffer's needs, not exact -
// that's fine, over-reserving once is cheap and this avoids undershooting.
func decodeBlockToArrowBatch(ctx context.Context, pool compression.ReaderPool, b []byte, format byte, symbolizer *symbolizer, numEntries, uncompressedSize int) (*log.ArrowBatch, error) {
	bi := newBufferedIterator(ctx, pool, b, format, symbolizer)
	defer bi.Close()

	bldr := array.NewStringBuilder(arrowAllocator)
	bldr.Reserve(numEntries)
	bldr.ReserveData(uncompressedSize)

	timestamps := make([]int64, 0, numEntries)
	structuredMetadata := make([]labels.Labels, 0, numEntries)
	hasStructuredMetadata := false

	for bi.Next() {
		// bi.currLine is sliced from bufferedIterator's reused decode
		// buffer, so it must be copied before the next Next() call.
		// BinaryBuilder.Append([]byte) copies it directly into the
		// builder's own values buffer - going through StringBuilder's
		// Append(string) would mean an extra string(...) copy plus
		// another []byte(...) copy back inside it, three copies total
		// for what only needs one.
		bldr.BinaryBuilder.Append(bi.currLine)
		timestamps = append(timestamps, bi.currTs)
		if !bi.currStructuredMetadata.IsEmpty() {
			hasStructuredMetadata = true
		}
		structuredMetadata = append(structuredMetadata, bi.currStructuredMetadata)
	}
	if err := bi.Err(); err != nil {
		return nil, err
	}

	batch := &log.ArrowBatch{
		Timestamps: timestamps,
		LineColumn: bldr.NewStringArray(),
	}
	if hasStructuredMetadata {
		batch.StructuredMetadata = structuredMetadata
	}
	return batch, nil
}

// entryBatchBufferedIterator is entryBufferedIterator's block-at-a-time
// counterpart: it decodes a whole compressed block into an ArrowBatch, runs
// the stream pipeline's ProcessBatch once for the block, then serves
// iter.EntryIterator's existing one-row-at-a-time contract by walking the
// result. Everything downstream (merge iterators, iter.ReadBatch) is
// unaffected by this: only the granularity at which the pipeline itself
// runs changes, not the iterator interface.
type entryBatchBufferedIterator struct {
	ctx              context.Context
	pool             compression.ReaderPool
	origBytes        []byte
	format           byte
	symbolizer       *symbolizer
	numEntries       int
	uncompressedSize int
	pipeline         log.StreamPipeline
	stats            *stats.Context

	decoded bool
	decErr  error
	batch   *log.ArrowBatch
	pos     int

	cur        logproto.Entry
	currLabels log.LabelsResult

	closed bool
}

func newBatchEntryIterator(ctx context.Context, pool compression.ReaderPool, b []byte, pipeline log.StreamPipeline, format byte, symbolizer *symbolizer, numEntries, uncompressedSize int) iter.EntryIterator {
	return &entryBatchBufferedIterator{
		ctx:              ctx,
		pool:             pool,
		origBytes:        b,
		format:           format,
		symbolizer:       symbolizer,
		numEntries:       numEntries,
		uncompressedSize: uncompressedSize,
		pipeline:         pipeline,
		stats:            stats.FromContext(ctx),
		pos:              -1,
	}
}

// ensureDecoded decodes the block and runs ProcessBatch on first use only.
func (e *entryBatchBufferedIterator) ensureDecoded() bool {
	if e.decoded {
		return e.decErr == nil
	}
	e.decoded = true

	block, err := decodeBlockToArrowBatch(e.ctx, e.pool, e.origBytes, e.format, e.symbolizer, e.numEntries, e.uncompressedSize)
	if err != nil {
		e.decErr = err
		return false
	}

	e.batch = e.pipeline.ProcessBatch(block)
	return true
}

func (e *entryBatchBufferedIterator) Next() bool {
	if e.closed || !e.ensureDecoded() {
		return false
	}

	e.pos++
	if e.pos >= e.batch.NumRows() {
		return false
	}

	row := e.batch.Selection[e.pos]
	e.cur.Timestamp = time.Unix(0, e.batch.Timestamps[row])
	e.cur.Line = string(e.batch.Lines[e.pos])
	e.currLabels = e.batch.Labels[e.pos]
	e.cur.StructuredMetadata = logproto.FromLabelsToLabelAdapters(e.currLabels.StructuredMetadata())
	e.cur.Parsed = logproto.FromLabelsToLabelAdapters(e.currLabels.Parsed())

	e.stats.AddPostFilterLines(1)
	return true
}

func (e *entryBatchBufferedIterator) At() logproto.Entry { return e.cur }

func (e *entryBatchBufferedIterator) Labels() string { return e.currLabels.String() }

func (e *entryBatchBufferedIterator) StreamHash() uint64 { return e.pipeline.BaseLabels().Hash() }

func (e *entryBatchBufferedIterator) Err() error { return e.decErr }

func (e *entryBatchBufferedIterator) Close() error {
	if e.closed {
		return e.decErr
	}
	e.closed = true

	if e.pipeline.ReferencedStructuredMetadata() {
		e.stats.SetQueryReferencedStructuredMetadata()
	}
	if e.batch != nil && e.batch.LineColumn != nil {
		e.batch.LineColumn.Release()
	}
	return e.decErr
}

// sampleBatchBufferedIterator is sampleBufferedIterator's block-at-a-time
// counterpart, mirroring entryBatchBufferedIterator but for
// log.StreamSampleExtractor.
type sampleBatchBufferedIterator struct {
	ctx              context.Context
	pool             compression.ReaderPool
	origBytes        []byte
	format           byte
	symbolizer       *symbolizer
	numEntries       int
	uncompressedSize int
	extractor        log.StreamSampleExtractor
	stats            *stats.Context
	hasher           util.SampleHasher

	decoded bool
	decErr  error
	batch   *log.ArrowBatch
	values  []float64
	pos     int

	curr       logproto.Sample
	currLabels log.LabelsResult
}

func newBatchSampleIterator(
	ctx context.Context,
	pool compression.ReaderPool,
	b []byte,
	format byte,
	symbolizer *symbolizer,
	extractor log.StreamSampleExtractor,
	numEntries, uncompressedSize int,
) iter.SampleIterator {
	if extractor == nil {
		return iter.NoopSampleIterator
	}

	return &sampleBatchBufferedIterator{
		ctx:              ctx,
		pool:             pool,
		origBytes:        b,
		format:           format,
		symbolizer:       symbolizer,
		numEntries:       numEntries,
		uncompressedSize: uncompressedSize,
		extractor:        extractor,
		stats:            stats.FromContext(ctx),
		pos:              -1,
	}
}

func (e *sampleBatchBufferedIterator) ensureDecoded() bool {
	if e.decoded {
		return e.decErr == nil
	}
	e.decoded = true

	block, err := decodeBlockToArrowBatch(e.ctx, e.pool, e.origBytes, e.format, e.symbolizer, e.numEntries, e.uncompressedSize)
	if err != nil {
		e.decErr = err
		return false
	}

	e.batch, e.values = e.extractor.ProcessBatch(block)
	return true
}

func (e *sampleBatchBufferedIterator) Next() bool {
	if !e.ensureDecoded() {
		return false
	}

	e.pos++
	if e.pos >= e.batch.NumRows() {
		return false
	}

	row := e.batch.Selection[e.pos]
	lr := e.batch.Labels[e.pos]
	e.currLabels = lr
	lblString := lr.String()

	line := e.batch.LineColumn.Value(int(row))
	e.curr = logproto.Sample{
		Timestamp: e.batch.Timestamps[row],
		Value:     e.values[e.pos],
		// Two entries in one stream can share a timestamp and a line but
		// extract different labels. Without the labels in the hash, the
		// merge iterator drops one as a duplicate.
		Hash: e.hasher.Hash(lblString, unsafeGetBytes(line)),
	}

	e.stats.AddPostFilterLines(1)
	return true
}

func (e *sampleBatchBufferedIterator) At() logproto.Sample { return e.curr }

func (e *sampleBatchBufferedIterator) Labels() string { return e.currLabels.String() }

func (e *sampleBatchBufferedIterator) StreamHash() uint64 { return e.extractor.BaseLabels().Hash() }

func (e *sampleBatchBufferedIterator) Err() error { return e.decErr }

func (e *sampleBatchBufferedIterator) Close() error {
	if e.extractor.ReferencedStructuredMetadata() {
		e.stats.SetQueryReferencedStructuredMetadata()
	}
	if e.batch != nil && e.batch.LineColumn != nil {
		e.batch.LineColumn.Release()
	}
	return e.decErr
}
