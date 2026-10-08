package chunkenc

import (
	"encoding/binary"
	"fmt"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
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
	tsLen := 8 * n
	offsetsLen := 4 * (n + 1)
	if n < 0 || len(data) < tsLen+offsetsLen {
		return nil, fmt.Errorf("columnar block of %d bytes too short for %d entries", len(data), n)
	}
	tsBytes := data[:tsLen]
	offsetsBytes := data[tsLen : tsLen+offsetsLen]
	rest := data[tsLen+offsetsLen:]

	// Validate the offsets, since array.String indexes the lines with them unchecked.
	offsets := arrow.Int32Traits.CastFromBytes(offsetsBytes)
	if offsets[0] != 0 {
		return nil, fmt.Errorf("first line offset is %d, expected 0", offsets[0])
	}
	for i := 1; i < len(offsets); i++ {
		if offsets[i] < offsets[i-1] {
			return nil, fmt.Errorf("line offset %d is %d, smaller than its predecessor %d", i, offsets[i], offsets[i-1])
		}
	}
	linesLen := int(offsets[n])
	if len(rest) < linesLen {
		return nil, fmt.Errorf("columnar block has %d bytes of lines, expected %d", len(rest), linesLen)
	}
	linesBytes := rest[:linesLen]
	rest = rest[linesLen:]

	// Timestamps are deltas, with the first being absolute.
	var ts int64
	for i := 0; i < tsLen; i += 8 {
		ts += int64(binary.LittleEndian.Uint64(tsBytes[i:]))
		binary.LittleEndian.PutUint64(tsBytes[i:], uint64(ts))
	}

	structuredMetadata, err := decodeStructuredMetadata(rest, n, symbolizer)
	if err != nil {
		return nil, err
	}

	tsData := array.NewData(arrow.PrimitiveTypes.Int64, n,
		[]*memory.Buffer{nil, memory.NewBufferBytes(tsBytes)}, nil, 0, 0)
	defer tsData.Release()

	linesData := array.NewData(arrow.BinaryTypes.String, n,
		[]*memory.Buffer{nil, memory.NewBufferBytes(offsetsBytes), memory.NewBufferBytes(linesBytes)}, nil, 0, 0)
	defer linesData.Release()

	return &log.ArrowBatch{
		Timestamps:         array.NewInt64Data(tsData),
		LineColumn:         array.NewStringData(linesData),
		StructuredMetadata: structuredMetadata,
	}, nil
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
