package chunkenc

import (
	"bytes"
	"context"
	"encoding/binary"
	"sync"
	"unsafe"

	//"fmt"
	"io"
	"time"

	"github.com/pierrec/lz4/v4"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/ronanh/intcomp"

	"github.com/grafana/loki/v3/pkg/logproto"
	"github.com/grafana/loki/v3/pkg/logql/log"
	"github.com/grafana/loki/v3/pkg/logqlmodel/stats"
)

// SlicePool uses a bucket pool and wraps the Get() and Put() functions for
// simpler access.
type SlicePool[T any] struct {
	p *sync.Pool
}

func NewSlicePool[T any](i int) *SlicePool[T] {
	return &SlicePool[T]{
		p: &sync.Pool{
			New: func() any {
				return make([]T, i)
			},
		},
	}
}

func (sp *SlicePool[T]) Get(n int) []T {
	b := sp.p.Get().([]T)
	return b[:n]
}

func (sp *SlicePool[T]) Put(buf []T) {
	sp.p.Put(buf[0:0])
}

var (
	decodePool      = NewSlicePool[byte](4 * 1024 * 1024)
	decodeInt64Pool = NewSlicePool[uint64](4 * 1024 * 1024)
)

type entryBatchIterator struct {
	*blockIterator
	pipeline log.StreamPipeline
	stats    *stats.Context

	cur        logproto.Entry
	currLabels log.LabelsResult

	curBatch *log.Batch
	curIndex int
}

func (e *entryBatchIterator) Entry() logproto.Entry {
	return e.cur
}

func (e *entryBatchIterator) Labels() string { return e.currLabels.String() }

func (e *entryBatchIterator) StreamHash() uint64 { return e.pipeline.BaseLabels().Hash() }

func (e *entryBatchIterator) Next() bool {
	if e.curBatch == nil {
		e.blockIterator.Load()
		e.curBatch = e.pipeline.ProcessBatch(e.blockIterator.batch)
		e.curIndex = -1
	}
	e.curIndex++
	if e.curIndex < len(e.batch.Selection) {
		ts, l, ok := e.batch.Get(e.batch.Selection[e.curIndex])
		e.cur.Timestamp = time.Unix(0, ts)
		// TODO: there must be a better way
		e.cur.Line = yoloString(l)
		return ok
	}

	return false
}

func yoloString(buf []byte) string {
	return *((*string)(unsafe.Pointer(&buf)))
}

func (e *entryBatchIterator) Close() error {
	if e.pipeline.ReferencedStructuredMetadata() {
		e.stats.SetQueryReferencedStructuredMetadata()
	}

	return e.blockIterator.Close()
}

type blockIterator struct {
	origBytes []byte

	// reader wraps origBytes
	reader io.Reader
	stats  *stats.Context

	// encReader reads compressed bytes from reader
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

	batch         *log.Batch
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

func (si *blockIterator) Load() {
	si.moveNext()
}

// moveNext moves the buffer to the next entry
func (si *blockIterator) moveNext() (int64, []byte, labels.Labels, bool) {
	if si.reader == nil {
		si.reader = bytes.NewReader(si.origBytes)
	}
	if si.encReader == nil {
		var err error
		si.encReader, err = si.pool.GetReader(si.reader)
		if err != nil {
			si.err = err
			return 0, nil, nil, false
		}
	}

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
	/*
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
	*/
	return ts, line, labels.EmptyLabels(), true
}

func (si *blockIterator) loadBatch() error {
	timestamps, err := DecodeVectorInt(si.reader)
	if err != nil {
		return err
	}

	entries, err := DecodeVectorString(si.reader)
	if err != nil {
		return err
	}

	si.batch = &log.Batch{
		Timestamps: timestamps,
		Entries:    entries,
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

	if si.batch != nil {
		decodePool.Put(si.batch.Entries.Lines)
		si.batch = nil
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

func EncodeVectorInt(vec log.VectorInt, w io.Writer) error {
	out := make([]uint64, 0, len(vec))
	out = intcomp.CompressDeltaVarByteInt64(vec, out)
	if err := binary.Write(w, binary.LittleEndian, int64(len(out))); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, out); err != nil {
		return err
	}
	return nil
}

func DecodeVectorInt(r io.Reader) (log.VectorInt, error) {
	var l int64
	if err := binary.Read(r, binary.LittleEndian, &l); err != nil {
		return nil, err
	}
	// TODO: use underlying raw bytes
	compressed := decodeInt64Pool.Get(int(l))
	defer decodeInt64Pool.Put(compressed)
	if err := binary.Read(r, binary.LittleEndian, compressed); err != nil {
		return nil, err
	}
	_, uncompressed := intcomp.UncompressDeltaVarByteInt64(compressed, make([]int64, 0, l))
	return uncompressed, nil
}

func EncodeVectorString(vec log.VectorString, w io.Writer) error {
	if err := EncodeVectorInt(vec.Offsets, w); err != nil {
		return err
	}

	if err := binary.Write(w, binary.LittleEndian, int64(len(vec.Lines))); err != nil {
		return err
	}
	// TODO: reuse compressor
	c := &lz4.Compressor{}
	// TODO: use pool for dst
	dst := make([]byte, lz4.CompressBlockBound(len(vec.Lines)))
	offset, err := c.CompressBlock(vec.Lines, dst)
	if err != nil {
		return err
	}
	dst = dst[:offset]
	if err := binary.Write(w, binary.LittleEndian, int64(offset)); err != nil {
		return err
	}
	if _, err := w.Write(dst); err != nil {
		return err
	}
	return nil
}

func DecodeVectorString(r io.Reader) (log.VectorString, error) {
	vec := log.VectorString{}
	var err error
	vec.Offsets, err = DecodeVectorInt(r)
	if err != nil {
		return vec, err
	}

	var l int64
	if err := binary.Read(r, binary.LittleEndian, &l); err != nil {
		return vec, err
	}
	var cl int64
	if err := binary.Read(r, binary.LittleEndian, &cl); err != nil {
		return vec, err
	}
	// TODO: we can avoid this allocation since the the underlying data is
	// already in memory
	compressed := decodePool.Get(int(cl))
	defer decodePool.Put(compressed)
	_, err = r.Read(compressed)
	if err != nil {
		return vec, err
	}
	vec.Lines = decodePool.Get(int(l))
	_, err = lz4.UncompressBlock(compressed, vec.Lines)
	return vec, err
}
