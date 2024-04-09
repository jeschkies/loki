package chunkenc

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/ronanh/intcomp"

	"github.com/grafana/loki/v3/pkg/logproto"
	"github.com/grafana/loki/v3/pkg/logql/log"
	"github.com/grafana/loki/v3/pkg/logqlmodel/stats"
)

type entryBatchIterator struct {
	*blockIterator
	pipeline log.StreamPipeline
	stats    *stats.Context

	cur        logproto.Entry
	currLabels log.LabelsResult
}

func (e *entryBatchIterator) Entry() logproto.Entry {
	return e.cur
}

func (e *entryBatchIterator) Labels() string { return e.currLabels.String() }

func (e *entryBatchIterator) StreamHash() uint64 { return e.pipeline.BaseLabels().Hash() }

func (e *entryBatchIterator) Next() bool {
	for e.blockIterator.Next() {
		// TODO: process batch instead going line by line
		newLine, lbs, matches := e.pipeline.Process(e.currTs, e.currLine, e.currStructuredMetadata...)
		if !matches {
			continue
		}

		e.stats.AddPostFilterLines(1)
		e.currLabels = lbs
		e.cur.Timestamp = time.Unix(0, e.currTs)
		e.cur.Line = string(newLine)
		e.cur.StructuredMetadata = logproto.FromLabelsToLabelAdapters(lbs.StructuredMetadata())
		e.cur.Parsed = logproto.FromLabelsToLabelAdapters(lbs.Parsed())

		return true
	}
	return false
}

func (e *entryBatchIterator) Close() error {
	if e.pipeline.ReferencedStructuredMetadata() {
		e.stats.SetQueryReferencedStructuredMetadata()
	}

	return e.blockIterator.Close()
}

type blockIterator struct {
	origBytes []byte
	stats     *stats.Context

	encReader  io.Reader
	pool       ReaderPool
	symbolizer *symbolizer

	err error

	readBuf      [20]byte // Enough bytes to store two varints.
	readBufValid int      // How many bytes are left in readBuf from previous read.

	format   byte
	currLine []byte // the current line, this is the same as the buffer but sliced the line size.
	currTs   int64

	symbolsBuf             []symbol      // The buffer for a single entry's symbols.
	currStructuredMetadata labels.Labels // The current labels.

	closed bool

	batch         *batch
	curBatchIndex int
}

func (si *blockIterator) Next() bool {
	if si.closed {
		return false
	}

	ts, line, structuredMetadata, ok := si.moveNext()
	if !ok {
		si.Close()
		return false
	}

	si.currTs = ts
	si.currLine = line
	si.currStructuredMetadata = structuredMetadata
	return true
}

// moveNext moves the buffer to the next entry
func (si *blockIterator) moveNext() (int64, []byte, labels.Labels, bool) {
	if si.batch == nil {
		err := si.loadBatch()
		if err != nil {
			si.err = err
			return 0, nil, nil, false
		}
	}

	ts, line, ok := si.batch.Get(si.curBatchIndex)
	if !ok {
		return 0, nil, nil, false
	}
	si.curBatchIndex++

	// Read symbolze

	lastAttempt := 0
	var symbolsSectionLengthWidth, nSymbolsWidth, nSymbols int
	for nSymbolsWidth == 0 { // Read until we have enough bytes for the labels.
		n, err := si.encReader.Read(si.readBuf[si.readBufValid:])
		si.readBufValid += n
		if err != nil {
			if err != io.EOF {
				si.err = err
				return 0, nil, nil, false
			}
			if si.readBufValid == 0 { // Got EOF and no data in the buffer.
				return 0, nil, nil, false
			}
			if si.readBufValid == lastAttempt { // Got EOF and could not parse same data last time.
				si.err = fmt.Errorf("invalid data in chunk")
				return 0, nil, nil, false
			}
		}
		var l uint64
		_, symbolsSectionLengthWidth = binary.Uvarint(si.readBuf[:si.readBufValid])
		l, nSymbolsWidth = binary.Uvarint(si.readBuf[symbolsSectionLengthWidth:si.readBufValid])
		nSymbols = int(l)
		lastAttempt = si.readBufValid
	}

	// Number of labels
	decompressedStructuredMetadataBytes := int64(binary.MaxVarintLen64)

	// Label symbols
	decompressedStructuredMetadataBytes += int64(nSymbols * 2 * binary.MaxVarintLen64)

	// Shift down what is still left in the fixed-size read buffer, if any.
	si.readBufValid = copy(si.readBuf[:], si.readBuf[symbolsSectionLengthWidth+nSymbolsWidth:si.readBufValid])

	// If not enough space for the symbols, create a new buffer slice and put the old one back in the pool.
	if nSymbols > cap(si.symbolsBuf) {
		if si.symbolsBuf != nil {
			SymbolsPool.Put(si.symbolsBuf)
		}
		si.symbolsBuf = SymbolsPool.Get(nSymbols).([]symbol)
		if nSymbols > cap(si.symbolsBuf) {
			si.err = fmt.Errorf("could not get a symbols matrix of size %d, actual %d", nSymbols, cap(si.symbolsBuf))
			return 0, nil, nil, false
		}
	}

	si.symbolsBuf = si.symbolsBuf[:nSymbols]

	// Read all the symbols, into the buffer.
	for i := 0; i < nSymbols; i++ {
		var sName, sValue uint64
		var nWidth, vWidth, lastAttempt int
		for vWidth == 0 { // Read until both varints have enough bytes.
			n, err := si.encReader.Read(si.readBuf[si.readBufValid:])
			si.readBufValid += n
			if err != nil {
				if err != io.EOF {
					si.err = err
					return 0, nil, nil, false
				}
				if si.readBufValid == 0 { // Got EOF and no data in the buffer.
					return 0, nil, nil, false
				}
				if si.readBufValid == lastAttempt { // Got EOF and could not parse same data last time.
					si.err = fmt.Errorf("invalid data in chunk")
					return 0, nil, nil, false
				}
			}
			sName, nWidth = binary.Uvarint(si.readBuf[:si.readBufValid])
			sValue, vWidth = binary.Uvarint(si.readBuf[nWidth:si.readBufValid])
			lastAttempt = si.readBufValid
		}

		// Shift down what is still left in the fixed-size read buffer, if any.
		si.readBufValid = copy(si.readBuf[:], si.readBuf[nWidth+vWidth:si.readBufValid])

		si.symbolsBuf[i].Name = uint32(sName)
		si.symbolsBuf[i].Value = uint32(sValue)
	}

	si.stats.AddDecompressedLines(1)
	si.stats.AddDecompressedStructuredMetadataBytes(decompressedStructuredMetadataBytes)
	si.stats.AddDecompressedBytes(decompressedStructuredMetadataBytes)

	return ts, line, si.symbolizer.Lookup(si.symbolsBuf[:nSymbols]), true
}

func (si *blockIterator) loadBatch() error {
	var err error
	reader := bytes.NewReader(si.origBytes)
	si.encReader, err = si.pool.GetReader(reader)
	if err != nil {
		return err
	}

	// Read timestamps
	tsLen, err := binary.ReadUvarint(reader)
	tsCompressed := make([]uint64, tsLen)
	for i := 0; i < int(tsLen); i++ {
		tsCompressed[i], err = binary.ReadUvarint(reader)
		if err != nil {
			return err
		}
	}
	_, timestamps := intcomp.UncompressDeltaVarByteInt64(tsCompressed, make([]int64, int(tsLen)))

	// Read offsets
	offsetsLen, err := binary.ReadUvarint(reader)
	if err != nil {
		return err
	}
	offsetsCompressed := make([]uint64, offsetsLen) // TODO: reuse tsCompressed
	for i := 0; i < int(offsetsLen); i++ {
		offsetsCompressed[i], err = binary.ReadUvarint(reader)
		if err != nil {
			return err
		}
	}
	_, offsets := intcomp.UncompressDeltaVarByteInt64(offsetsCompressed, make([]int64, int(offsetsLen)))

	// Read lines
	linesSize, err := binary.ReadUvarint(reader)
	if err != nil {
		return err
	}
	lines := make([]byte, linesSize)
	_, err = si.encReader.Read(lines)
	if err != nil {
		return err
	}
	// panic(n!=lineSize)

	si.batch = &batch{
		timestamps: timestamps,
		offsets:    offsets,
		lines:      lines,
	}

	return nil
}

func (si *blockIterator) Error() error { return si.err }

func (si *blockIterator) Close() error {
	if !si.closed {
		si.closed = true
		si.close()
	}
	return si.err
}

func (si *blockIterator) close() {
	if si.encReader != nil {
		si.pool.PutReader(si.encReader)
		si.encReader = nil
	}

	if si.symbolsBuf != nil {
		SymbolsPool.Put(si.symbolsBuf)
		si.symbolsBuf = nil
	}

	si.origBytes = nil
}

func newBlockIterator(ctx context.Context, pool ReaderPool, b []byte, format byte, symbolizer *symbolizer) *blockIterator {
	stats := stats.FromContext(ctx)
	stats.AddCompressedBytes(int64(len(b)))
	return &blockIterator{
		stats:      stats,
		origBytes:  b,
		encReader:  nil, // will be initialized later
		pool:       pool,
		format:     format,
		symbolizer: symbolizer,
	}
}

type batch struct {
	timestamps []int64
	offsets    []int64
	lines      []byte
}

// Returns the timestamp and line for index i or false
func (b *batch) Get(i int) (int64, []byte, bool) {
	if i < 0 || i >= len(b.timestamps) {
		return 0, nil, false
	}

	prevOffset := 0
	if i > 0 {
		prevOffset = int(b.offsets[i-1])
	}
	return b.timestamps[i], b.lines[prevOffset:b.offsets[i]], true
}

func (b *batch) Append(ts int64, line []byte) {
	b.timestamps = append(b.timestamps, ts)
	b.offsets = append(b.offsets, int64(len(b.lines)))
	b.lines = append(b.lines, line...)
}
