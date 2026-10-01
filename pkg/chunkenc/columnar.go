package chunkenc

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	lz4lib "github.com/pierrec/lz4/v4"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/grafana/loki/v3/pkg/logproto"
	"github.com/grafana/loki/v3/pkg/logql/log"
	"github.com/grafana/loki/v3/pkg/logqlmodel/stats"
)

// ChunkFormatV5 stores a block's entries in a columnar layout instead of
// the row-major, per-entry-interleaved layout every earlier format uses:
//
//	[uvarint tsLen]   [tsLen bytes: hand-rolled delta-of-delta timestamps]
//	[uvarint llLen]   [llLen bytes: sequential uvarint line lengths]
//	[uvarint rawLen] [uvarint compLen] [compLen bytes: LZ4 raw-block-compressed line bytes]
//	[per-entry structured metadata symbol sections, unchanged from earlier formats]
//
// Only the line bytes are LZ4-compressed. Timestamps and line lengths are
// left as their own (already compact) encodings - superimposing a general
// byte compressor on already delta/varint-packed integers earns little.
// Because the whole line blob is read and decompressed in one shot (raw
// LZ4 block format, not the streaming frame format the other chunk
// formats use - see compression.GetReaderPool/GetWriterPool), and offsets
// are derived once instead of parsed one entry at a time, decode largely
// avoids the tiny-read-through-bufio overhead that dominates decoding
// every earlier format (see batch.go/decodeBlockToArrowBatch's doc
// comment and the profiling that motivated this format).
const ChunkFormatV5 = ChunkFormatV4 + 1

func encodeColumnarBlock(timestamps []int64, lines []string, symbolsPerEntry []symbols) ([]byte, error) {
	tsBytes := encodeTimestampsDoD(timestamps)

	tmp := make([]byte, binary.MaxVarintLen64)
	lineLenBuf := make([]byte, 0, len(lines)*2)
	totalLineBytes := 0
	for _, l := range lines {
		n := binary.PutUvarint(tmp, uint64(len(l)))
		lineLenBuf = append(lineLenBuf, tmp[:n]...)
		totalLineBytes += len(l)
	}

	linesBlob := make([]byte, 0, totalLineBytes)
	for _, l := range lines {
		linesBlob = append(linesBlob, l...)
	}

	compressed := make([]byte, lz4lib.CompressBlockBound(len(linesBlob)))
	n, err := lz4lib.CompressBlock(linesBlob, compressed, nil)
	if err != nil {
		return nil, fmt.Errorf("lz4 compress line blob: %w", err)
	}
	compressed = compressed[:n]

	out := make([]byte, 0, len(tsBytes)+len(lineLenBuf)+len(compressed)+64)
	out = appendUvarint(out, uint64(len(tsBytes)))
	out = append(out, tsBytes...)
	out = appendUvarint(out, uint64(len(lineLenBuf)))
	out = append(out, lineLenBuf...)
	out = appendUvarint(out, uint64(totalLineBytes))
	out = appendUvarint(out, uint64(len(compressed)))
	out = append(out, compressed...)

	for _, syms := range symbolsPerEntry {
		out = appendUvarint(out, uint64(len(syms)))
		for _, s := range syms {
			out = appendUvarint(out, uint64(s.Name))
			out = appendUvarint(out, uint64(s.Value))
		}
	}

	return out, nil
}

func appendUvarint(dst []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(dst, tmp[:n]...)
}

// serialiseColumnarBlock collects hb's entries (in append/write order, not
// necessarily timestamp order - matching what every other Serialise
// implementation in this package does, since sorting happens at query
// time, not write time) and encodes them via encodeColumnarBlock.
func serialiseColumnarBlock(hb *unorderedHeadBlock) ([]byte, error) {
	timestamps := make([]int64, 0, hb.lines)
	lines := make([]string, 0, hb.lines)
	symbolsPerEntry := make([]symbols, 0, hb.lines)

	err := hb.forEntries(
		context.Background(),
		logproto.FORWARD,
		0,
		math.MaxInt64,
		func(_ *stats.Context, ts int64, line string, structuredMetadataSymbols symbols) error {
			timestamps = append(timestamps, ts)
			lines = append(lines, line)
			symbolsPerEntry = append(symbolsPerEntry, structuredMetadataSymbols)
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	return encodeColumnarBlock(timestamps, lines, symbolsPerEntry)
}

// decodeColumnarBlockToArrowBatch is decodeBlockToArrowBatch's counterpart
// for ChunkFormatV5's columnar block layout: it decompresses/unpacks the
// three columns in three bulk operations (no per-entry small reads at all)
// and builds the resulting log.ArrowBatch from them.
func decodeColumnarBlockToArrowBatch(ctx context.Context, b []byte, numEntries int, symbolizer *symbolizer) (*log.ArrowBatch, error) {
	chunkStats := stats.FromContext(ctx)
	chunkStats.AddCompressedBytes(int64(len(b)))

	pos := 0

	// timestampsBuf/valuesBuf/offsetsBuf are all pooled, arrowAllocator-
	// backed buffers (see their own allocation sites below) that need to
	// be returned to the pool if this function returns early with an
	// error - a truncated/corrupt block can fail at any point after one
	// or more of them has already been allocated. success is set just
	// before the final, non-error return; until then, this defer treats
	// every return as an error path.
	var timestampsBuf, valuesBuf, offsetsBuf *memory.Buffer
	success := false
	defer func() {
		if success {
			return
		}
		for _, buf := range [...]*memory.Buffer{timestampsBuf, valuesBuf, offsetsBuf} {
			if buf != nil {
				buf.Release()
			}
		}
	}()

	readUvarint := func() (uint64, error) {
		v, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return 0, fmt.Errorf("invalid columnar block: truncated uvarint at offset %d", pos)
		}
		pos += n
		return v, nil
	}

	// readSection reads a uvarint-prefixed length, then returns that
	// many bytes starting at pos, advancing pos past them. It checks
	// the length against b's remaining bytes before slicing: a
	// corrupted or truncated block can make that length arbitrarily
	// large, and b[pos:pos+length] would otherwise either panic (if
	// the requested end exceeds cap(b)) or silently read into whatever
	// garbage follows (if it's still within cap(b) but past the data
	// actually meant to be there - cap(b) is not under this function's
	// control, so len(b) is the only bound it can trust).
	readSection := func() ([]byte, error) {
		length, err := readUvarint()
		if err != nil {
			return nil, err
		}
		if length > uint64(len(b)-pos) {
			return nil, fmt.Errorf("invalid columnar block: truncated section at offset %d: need %d bytes, have %d", pos, length, len(b)-pos)
		}
		section := b[pos : pos+int(length)]
		pos += int(length)
		return section, nil
	}

	tsBytes, err := readSection()
	if err != nil {
		return nil, err
	}

	// Same pooling trick as valuesBuf/offsetsBuf below: decode straight
	// into a pooled, arrowAllocator-backed buffer instead of the plain
	// make([]int64, n) a nil dst would force decodeTimestampsDoD to
	// allocate internally - decodeTimestampsDoD already supports
	// decoding in place into a reused dst, nil just never took advantage
	// of that. Once the resulting array's/batch's refcount hits zero,
	// ArrowBatch.Release returns this buffer to the pool via
	// TimestampsBuf, instead of it becoming unpooled GC garbage.
	var timestamps []int64
	if numEntries > 0 {
		timestampsBuf = memory.NewResizableBuffer(arrowAllocator)
		timestampsBuf.Resize(numEntries * arrow.Int64SizeBytes)
		timestamps = decodeTimestampsDoD(tsBytes, numEntries, arrow.Int64Traits.CastFromBytes(timestampsBuf.Bytes()))
	} else {
		timestamps = decodeTimestampsDoD(tsBytes, numEntries, nil)
	}

	llLen, err := readUvarint()
	if err != nil {
		return nil, err
	}
	if llLen > uint64(len(b)-pos) {
		return nil, fmt.Errorf("invalid columnar block: truncated line-lengths section at offset %d: need %d bytes, have %d", pos, llLen, len(b)-pos)
	}
	llEnd := pos + int(llLen)

	lineLengths := make([]int, numEntries)
	totalLineBytes := 0
	for i := 0; i < numEntries; i++ {
		v, n := binary.Uvarint(b[pos:llEnd])
		if n <= 0 {
			return nil, fmt.Errorf("invalid columnar block: truncated line length at entry %d", i)
		}
		pos += n
		lineLengths[i] = int(v)
		totalLineBytes += int(v)
	}
	pos = llEnd

	rawLen, err := readUvarint()
	if err != nil {
		return nil, err
	}
	compressed, err := readSection()
	if err != nil {
		return nil, err
	}

	if totalLineBytes != int(rawLen) {
		return nil, fmt.Errorf("invalid columnar block: line lengths sum to %d, uncompressed blob length says %d", totalLineBytes, rawLen)
	}

	// Decompress directly into a pooled, allocator-backed buffer instead
	// of a plain make() + wrap: Buffer.Resize allocates via arrowAllocator
	// (pulling from arrowBufferPool, same pool the old StringBuilder path
	// used), and Buffer.Bytes() returns a view into that same backing
	// array, not a copy - so lz4.UncompressBlock can write straight into
	// it. Once the resulting String array's refcount hits zero,
	// Release() returns this buffer to the pool via arrowAllocator.Free()
	// instead of it becoming unpooled garbage, which a plain make()
	// wrapped in memory.NewBufferBytes (the array's earlier approach)
	// could never do - NewBufferBytes-backed buffers have no allocator to
	// free back to, by design.
	valuesBuf = memory.NewResizableBuffer(arrowAllocator)
	valuesBuf.Resize(int(rawLen))
	linesBlob := valuesBuf.Bytes()
	if rawLen > 0 {
		n, err := lz4lib.UncompressBlock(compressed, linesBlob)
		if err != nil {
			return nil, fmt.Errorf("lz4 uncompress line blob: %w", err)
		}
		if n != int(rawLen) {
			return nil, fmt.Errorf("lz4 uncompress line blob: got %d bytes, want %d", n, rawLen)
		}
	}

	chunkStats.AddDecompressedLines(int64(numEntries))
	chunkStats.AddDecompressedBytes(int64(len(tsBytes)) + int64(llLen) + int64(rawLen))

	// Same pooling trick for the offsets buffer: allocate it through
	// arrowAllocator and get a writable []int32 view over its raw bytes
	// (arrow.Int32Traits.CastFromBytes), instead of a separate unpooled
	// make([]int32, ...) computed into and then copied out of.
	offsetsBuf = memory.NewResizableBuffer(arrowAllocator)
	offsetsBuf.Resize((numEntries + 1) * arrow.Int32SizeBytes)
	lineOffsets := arrow.Int32Traits.CastFromBytes(offsetsBuf.Bytes())
	// The prefix sum below only ever writes lineOffsets[i+1], relying on
	// lineOffsets[0] == 0 as its implicit base case. That's guaranteed for
	// a fresh make([]int32, ...) (Go zero-initializes new memory), but
	// offsetsBuf's bytes come from a pooled, reused allocator buffer that
	// can hold leftover garbage from whatever previously occupied that
	// slot - unlike valuesBuf, which lz4.UncompressBlock overwrites in
	// full, nothing here writes index 0 unless we do it explicitly.
	lineOffsets[0] = 0
	for i, l := range lineLengths {
		lineOffsets[i+1] = lineOffsets[i] + int32(l)
	}

	// POC: collect structured metadata as raw symbol IDs in flat
	// offsets+names+values slices, instead of eagerly resolving each
	// row into its own heap-allocated labels.Labels here (symbolizer.
	// Lookup's RWMutex-guarded cache lookups, OTel name normalization,
	// and ScratchBuilder.Labels() pack, paid for every row regardless
	// of whether anything downstream ever reads that row's metadata).
	// Resolution now happens lazily, via symbolizer.LookupIDs, only
	// for rows ArrowBatch.get is actually called for - see the design
	// discussion this is based on and ArrowBatch.SymbolTable's doc
	// comment. symOffsets mirrors lineOffsets' own prefix-sum shape
	// just above.
	symOffsets := make([]int32, numEntries+1)
	var symNames, symValues []uint32
	var structuredMetadataBytes int64

	for i := 0; i < numEntries; i++ {
		nSymbols, err := readUvarint()
		if err != nil {
			return nil, err
		}
		if nSymbols > 0 {
			structuredMetadataBytes += int64(nSymbols) * 2 * binary.MaxVarintLen64
		}
		for j := uint64(0); j < nSymbols; j++ {
			name, err := readUvarint()
			if err != nil {
				return nil, err
			}
			value, err := readUvarint()
			if err != nil {
				return nil, err
			}
			symNames = append(symNames, uint32(name))
			symValues = append(symValues, uint32(value))
		}
		symOffsets[i+1] = symOffsets[i] + int32(nSymbols)
	}

	chunkStats.AddDecompressedStructuredMetadataBytes(structuredMetadataBytes)
	chunkStats.AddDecompressedBytes(structuredMetadataBytes)

	lineColumn := newStringArrayFromBuffers(numEntries, offsetsBuf, valuesBuf)
	// newStringArrayFromBuffers already released both buffers itself,
	// transferring ownership to lineColumn's own retained references -
	// clear them so the deferred cleanup above can't double-release
	// them if a future change adds an error path below this point.
	offsetsBuf, valuesBuf = nil, nil

	batch := &log.ArrowBatch{
		Timestamps:    timestamps,
		TimestampsBuf: timestampsBuf,
		LineColumn:    lineColumn,
	}
	if len(symNames) > 0 {
		offB := array.NewInt32Builder(arrowAllocator)
		offB.AppendValues(symOffsets, nil)
		batch.StructuredMetadataOffsets = offB.NewInt32Array()
		offB.Release()

		nameB := array.NewUint32Builder(arrowAllocator)
		nameB.AppendValues(symNames, nil)
		batch.StructuredMetadataNames = nameB.NewUint32Array()
		nameB.Release()

		valB := array.NewUint32Builder(arrowAllocator)
		valB.AppendValues(symValues, nil)
		batch.StructuredMetadataValues = valB.NewUint32Array()
		valB.Release()

		batch.SymbolTable = symbolizer
	}
	success = true
	return batch, nil
}

// newStringArrayFromBuffers builds an Arrow String array directly from an
// already-populated int32 offsets buffer (numRows+1 values) and an
// already-populated values buffer, with no validity/null bitmap (every
// row, including zero-length lines, is a valid empty string, never SQL
// NULL - exactly what every per-line Append call elsewhere in this
// package already produces).
//
// Both buffers are taken by reference, not copied: array.NewData retains
// its own reference to each, so this releases its caller's reference
// afterward (the same create-with-refcount-1, retain-on-handoff,
// release-your-own-copy pattern BinaryBuilder.newData uses internally) -
// whatever allocator each buffer came from (e.g. arrowAllocator, so it
// returns to the pool once the resulting array's refcount hits zero) is
// entirely up to the caller.
func newStringArrayFromBuffers(numRows int, offsetsBuf, valuesBuf *memory.Buffer) *array.String {
	data := array.NewData(arrow.BinaryTypes.String, numRows, []*memory.Buffer{nil, offsetsBuf, valuesBuf}, nil, 0, 0)
	offsetsBuf.Release()
	valuesBuf.Release()
	defer data.Release()

	return array.NewStringData(data)
}
