package chunkenc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/grafana/loki/v3/pkg/compression"
	"github.com/grafana/loki/v3/pkg/logql/log"
)

// symbolTable is the read-only symbol table of a chunk, kept as the bytes it
// was serialised to instead of one string per symbol like symbolizer.
//
// The decompressed bytes are the buffer of the German strings in the structured
// metadata of every block of the chunk, so a block only builds the headers of
// its pairs and never copies a symbol. Symbols up to 12 bytes are inline in their
// header anyway.
type symbolTable struct {
	// data is the decompressed table: for every symbol its length as uvarint and its bytes.
	data *memory.Buffer
	// headers[i] is the header of symbol i, referring to data as buffer 0.
	headers []log.PackedHeader
	// needsNormalizing[i] is true if symbol i is not trivially a valid label
	// name. Used as a name it must be normalised first, like symbolizer.Lookup does.
	needsNormalizing []bool
}

// symbolTableFromEnc builds the symbolTable from the bytes written by
// symbolizer.SerializeTo.
func symbolTableFromEnc(b []byte, pool compression.ReaderPool) (*symbolTable, error) {
	db := decbuf{b: b}
	numSymbols := db.uvarint()
	if err := db.err(); err != nil {
		return nil, fmt.Errorf("read number of symbols: %w", err)
	}

	reader, err := pool.GetReader(bytes.NewReader(db.b))
	if err != nil {
		return nil, err
	}
	defer pool.PutReader(reader)

	// TODO: read into a pooled buffer. The size is not known before reading.
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("decompress symbols: %w", err)
	}

	return newSymbolTable(data, numSymbols)
}

// newSymbolTable parses the numSymbols symbols in data, which it takes over.
func newSymbolTable(data []byte, numSymbols int) (*symbolTable, error) {
	t := &symbolTable{
		data:             memory.NewBufferBytes(data),
		headers:          make([]log.PackedHeader, numSymbols),
		needsNormalizing: make([]bool, numSymbols),
	}

	pos := 0
	for i := range t.headers {
		size, width := binary.Uvarint(data[pos:])
		if width <= 0 || size > uint64(len(data)-pos-width) {
			return nil, fmt.Errorf("symbol %d of %d is truncated", i, numSymbols)
		}
		pos += width
		symbol := data[pos : pos+int(size)]

		t.headers[i] = log.NewPackedHeader(symbol, 0, int32(pos))
		t.needsNormalizing[i] = !isPlainLabelName(symbol)
		pos += int(size)
	}
	if pos != len(data) {
		return nil, fmt.Errorf("symbol table has %d trailing bytes", len(data)-pos)
	}
	return t, nil
}

// isPlainLabelName reports whether LabelNamer.Build leaves name unchanged. It
// is conservative: letters, digits and single underscores in between, starting
// with a letter. Anything else takes the slow path, which asks the LabelNamer.
func isPlainLabelName(name []byte) bool {
	if len(name) == 0 {
		return false
	}
	prevUnderscore := false
	for i, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			prevUnderscore = false
		case c >= '0' && c <= '9' && i > 0:
			prevUnderscore = false
		case c == '_' && i > 0 && !prevUnderscore:
			prevUnderscore = true
		default:
			return false
		}
	}
	return !prevUnderscore
}
