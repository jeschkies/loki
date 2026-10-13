package chunkenc

import (
	"encoding/binary"
	"fmt"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/prometheus/otlptranslator"
	"github.com/prometheus/prometheus/model/labels"

	"github.com/grafana/loki/v3/pkg/logql/log"
)

// decodeColumnarBlock decodes the n entries of a decompressed columnar block, as written by
// SerialiseColumnar, into an ArrowBatch with all rows selected.
//
// The batch does not copy the data. Its timestamps and lines point into data, so data must not be
// reused while the batch is in use. The timestamps are turned from deltas into absolute values in
// place, which means data can only be decoded once. The arrays assume a little-endian host, as
// the block is little-endian.
//
// If symbolizer is nil the block has no structured metadata and the rows get empty labels.
// Otherwise the symbol columns are resolved to labels with it.
//
// nolint:unused
func decodeColumnarBlock(data []byte, n int, symbolizer *symbolizer) (*log.ArrowBatch, error) {
	batch, rest, err := decodeColumnarLines(data, n)
	if err != nil {
		return nil, err
	}

	batch.StructuredMetadata, err = decodeStructuredMetadata(rest, n, symbolizer)
	if err != nil {
		batch.Timestamps.Release()
		batch.LineColumn.Release()
		return nil, err
	}
	return batch, nil
}

// decodeColumnarBlockView is decodeColumnarBlock with the structured metadata in
// ArrowBatch.StructuredMetadataView instead of ArrowBatch.StructuredMetadata.
//
// The view refers to the symbols in table. A block only allocates the headers of
// its pairs and the offsets of its rows. If table is nil the block has no
// structured metadata and the view has no pairs.
//
// nolint:unused
func decodeColumnarBlockView(data []byte, n int, table *symbolTable) (*log.ArrowBatch, error) {
	batch, rest, err := decodeColumnarLines(data, n)
	if err != nil {
		return nil, err
	}

	batch.StructuredMetadataView, err = decodeStructuredMetadataView(rest, n, table)
	if err != nil {
		batch.Timestamps.Release()
		batch.LineColumn.Release()
		return nil, err
	}
	return batch, nil
}

// decodeColumnarLines decodes the timestamps and lines of a block into a batch with all rows
// selected and returns the rest of the block, which are the structured metadata columns.
func decodeColumnarLines(data []byte, n int) (*log.ArrowBatch, []byte, error) {
	tsLen := 8 * n
	offsetsLen := 4 * (n + 1)
	if n < 0 || len(data) < tsLen+offsetsLen {
		return nil, nil, fmt.Errorf("columnar block of %d bytes too short for %d entries", len(data), n)
	}
	tsBytes := data[:tsLen]
	offsetsBytes := data[tsLen : tsLen+offsetsLen]
	rest := data[tsLen+offsetsLen:]

	// Validate the offsets, since array.String indexes the lines with them unchecked.
	offsets := arrow.Int32Traits.CastFromBytes(offsetsBytes)
	if offsets[0] != 0 {
		return nil, nil, fmt.Errorf("first line offset is %d, expected 0", offsets[0])
	}
	for i := 1; i < len(offsets); i++ {
		if offsets[i] < offsets[i-1] {
			return nil, nil, fmt.Errorf("line offset %d is %d, smaller than its predecessor %d", i, offsets[i], offsets[i-1])
		}
	}
	linesLen := int(offsets[n])
	if len(rest) < linesLen {
		return nil, nil, fmt.Errorf("columnar block has %d bytes of lines, expected %d", len(rest), linesLen)
	}
	linesBytes := rest[:linesLen]
	rest = rest[linesLen:]

	// Timestamps are deltas, with the first being absolute.
	var ts int64
	for i := 0; i < tsLen; i += 8 {
		ts += int64(binary.LittleEndian.Uint64(tsBytes[i:]))
		binary.LittleEndian.PutUint64(tsBytes[i:], uint64(ts))
	}

	tsData := array.NewData(arrow.PrimitiveTypes.Int64, n,
		[]*memory.Buffer{nil, memory.NewBufferBytes(tsBytes)}, nil, 0, 0)
	defer tsData.Release()

	linesData := array.NewData(arrow.BinaryTypes.String, n,
		[]*memory.Buffer{nil, memory.NewBufferBytes(offsetsBytes), memory.NewBufferBytes(linesBytes)}, nil, 0, 0)
	defer linesData.Release()

	return &log.ArrowBatch{
		Timestamps: array.NewInt64Data(tsData),
		LineColumn: array.NewStringData(linesData),
	}, rest, nil
}

// decodeStructuredMetadataView turns the symbol counts and symbols columns of n entries into a
// Map<StringView, StringView> of label names to values. Its headers are copied from table and refer
// to the symbols in it.
func decodeStructuredMetadataView(b []byte, n int, table *symbolTable) (*array.Map, error) {
	if table == nil {
		return log.NewStructuredMetadataMap(make([]int32, n+1), nil, nil), nil
	}

	countsLen := 4 * n
	if len(b) < countsLen {
		return nil, fmt.Errorf("columnar block has %d bytes of symbol counts, expected %d", len(b), countsLen)
	}
	counts := b[:countsLen]
	symbolBytes := b[countsLen:]
	if len(symbolBytes)%8 != 0 {
		return nil, fmt.Errorf("columnar block has %d bytes of symbols, expected a multiple of 8", len(symbolBytes))
	}
	pairs := len(symbolBytes) / 8

	// The counts are the lengths of the rows. The Map wants their offsets.
	offsets := make([]int32, n+1)
	total := 0
	for i := 0; i < n; i++ {
		total += int(binary.LittleEndian.Uint32(counts[4*i:]))
		if total > pairs {
			return nil, fmt.Errorf("entry %d ends at symbol %d, only %d in the block", i, total, pairs)
		}
		offsets[i+1] = int32(total)
	}
	if total != pairs {
		return nil, fmt.Errorf("columnar block has %d trailing bytes of symbols", 8*(pairs-total))
	}

	var (
		keys    = make([]log.PackedHeader, pairs)
		values  = make([]log.PackedHeader, pairs)
		headers = table.headers
		numSyms = uint32(len(headers))
		names   normalizedNames
	)
	for j := 0; j < pairs; j++ {
		name := binary.LittleEndian.Uint32(symbolBytes[8*j:])
		value := binary.LittleEndian.Uint32(symbolBytes[8*j+4:])
		if name >= numSyms || value >= numSyms {
			return nil, fmt.Errorf("symbols %d and %d are out of range, the table has %d", name, value, numSyms)
		}

		values[j] = headers[value]
		if !table.needsNormalizing[name] {
			keys[j] = headers[name]
			continue
		}
		h, err := names.header(table, name)
		if err != nil {
			return nil, fmt.Errorf("normalize label name of symbol %d: %w", name, err)
		}
		keys[j] = h
	}

	buffers := []*memory.Buffer{table.data}
	if len(names.extra) > 0 {
		buffers = append(buffers, memory.NewBufferBytes(names.extra))
	}
	return log.NewStructuredMetadataMap(offsets, keys, values, buffers...), nil
}

// normalizedNames holds the label names of a block that symbolizer.Lookup would have changed. They
// do not exist in the symbol table, so they are appended to extra, which is the block's second
// data buffer. This is the slow path. Names of OTLP attributes like "service.name" take it.
type normalizedNames struct {
	namer   otlptranslator.LabelNamer
	headers map[uint32]log.PackedHeader
	extra   []byte
}

func (n *normalizedNames) header(table *symbolTable, name uint32) (log.PackedHeader, error) {
	if h, ok := n.headers[name]; ok {
		return h, nil
	}

	raw := table.headers[name]
	if raw.Length() == 0 {
		// symbolizer.Lookup keeps an empty name.
		return raw, nil
	}
	var (
		inline [log.PackedHeaderSizeBytes]byte
		symbol []byte
	)
	if raw.IsInline() {
		// Bytes 4 to 15 of the header are the symbol.
		binary.LittleEndian.PutUint64(inline[0:], raw.Lo)
		binary.LittleEndian.PutUint64(inline[8:], raw.Hi)
		symbol = inline[4 : 4+raw.Length()]
	} else {
		symbol = table.data.Bytes()[raw.BufferOffset() : raw.BufferOffset()+raw.Length()]
	}

	normalized, err := n.namer.Build(string(symbol))
	if err != nil {
		return log.PackedHeader{}, err
	}
	h := raw
	if normalized != string(symbol) {
		h = log.NewPackedHeader([]byte(normalized), 1, int32(len(n.extra)))
		n.extra = append(n.extra, normalized...)
	}
	if n.headers == nil {
		n.headers = map[uint32]log.PackedHeader{}
	}
	n.headers[name] = h
	return h, nil
}

// decodeStructuredMetadata resolves the symbol counts and symbols columns of n entries to labels.
// It returns n empty labels if symbolizer is nil.
func decodeStructuredMetadata(b []byte, n int, symbolizer *symbolizer) ([]labels.Labels, error) {
	result := make([]labels.Labels, n)
	if symbolizer == nil {
		for i := range result {
			result[i] = labels.EmptyLabels()
		}
		return result, nil
	}

	countsLen := 4 * n
	if len(b) < countsLen {
		return nil, fmt.Errorf("columnar block has %d bytes of symbol counts, expected %d", len(b), countsLen)
	}
	counts := b[:countsLen]
	symbolBytes := b[countsLen:]
	if len(symbolBytes)%8 != 0 {
		return nil, fmt.Errorf("columnar block has %d bytes of symbols, expected a multiple of 8", len(symbolBytes))
	}

	var (
		syms    symbols
		scratch labels.ScratchBuilder
	)
	for i := range result {
		count := int(binary.LittleEndian.Uint32(counts[4*i:]))
		if count > len(symbolBytes)/8 {
			return nil, fmt.Errorf("entry %d has %d symbols, only %d left", i, count, len(symbolBytes)/8)
		}

		syms = syms[:0]
		for j := 0; j < count; j++ {
			syms = append(syms, symbol{
				Name:  binary.LittleEndian.Uint32(symbolBytes[8*j:]),
				Value: binary.LittleEndian.Uint32(symbolBytes[8*j+4:]),
			})
		}
		symbolBytes = symbolBytes[8*count:]

		lbls, err := symbolizer.Lookup(syms, &scratch)
		if err != nil {
			return nil, fmt.Errorf("resolve structured metadata of entry %d: %w", i, err)
		}
		result[i] = lbls
	}
	if len(symbolBytes) != 0 {
		return nil, fmt.Errorf("columnar block has %d trailing bytes of symbols", len(symbolBytes))
	}
	return result, nil
}
