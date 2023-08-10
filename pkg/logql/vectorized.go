package logql

import (
	//"fmt"
	"bytes"

	"github.com/grafana/regexp"
	memmem "github.com/jeschkies/go-memmem/pkg/search"
)

type Vec interface {
	Bytes() []byte
	Int() []int64
}

type bytesVec []byte

func (b bytesVec) Bytes() []byte {
	return b
}

func (bytesVec) Int() []int64 {
	return nil
}

type intVec []int64

func (intVec) Bytes() []byte {
	return nil
}

func (i intVec) Int() []int64 {
	return i
}

// NewLinesVec returns three vectors: bytes, start and end indices for each entry.
func NewEntriesVec(data []byte) []Vec {
	start := int64(0)
	startIndices := make([]int64, 0)
	endIndices := make([]int64, 0)
	for start < int64(len(data)-1) {
		startIndices = append(startIndices, start)
		next := int64(bytes.IndexByte(data[start:], '\n'))
		if next == -1 {
			endIndices = append(endIndices, int64(len(data)))
			break
		}
		start += next + 1 // jump over \n
		endIndices = append(endIndices, start)
	}
	return []Vec{bytesVec(data), intVec(startIndices), intVec(endIndices)}
}

type Batch interface {
	ColVec(i int) Vec
	Selection() []int
	WithSelection(selection []int) Batch
}

type mutableBatch struct {
	vectors   []Vec
	selection []int
}

func NewBatch(vectors []Vec) Batch {
	return &mutableBatch{
		vectors: vectors,
	}
}

func (b *mutableBatch) ColVec(i int) Vec {
	return b.vectors[i]
}

func (b *mutableBatch) Selection() []int {
	return b.selection
}

func (b *mutableBatch) WithSelection(selection []int) Batch {
	return &mutableBatch{
		vectors:   b.vectors,
		selection: selection,
	}
}

func VecFilter(input Batch, col int, needle []byte) Batch {
	vec := input.ColVec(col)
	haystack := vec.Bytes()
	entryPositions := input.ColVec(1).Int()

	selection := make([]int, 0)
	i := 0
	for i < len(entryPositions) {
		start := entryPositions[i]
		offset := memmem.Index(haystack[start:], needle)
		if offset == -1 {
			break
		}

		// Find first start that is above pos.
		// TODO: We can probably be branchless here.
		for entryPositions[i] <= start+offset {
			i++
		}
		selection = append(selection, i-1)
	}

	return input.WithSelection(selection)
}

func VecRegexp(input Batch, col int, r *regexp.Regexp) Batch {
	vec := input.ColVec(col)
	haystack := vec.Bytes()
	entryPositions := input.ColVec(1).Int()

	updatedSelection := make([]int, 0)
	for _, selIndex := range input.Selection() {
		var s []byte
		if selIndex < len(entryPositions)-1 {
			s = haystack[entryPositions[selIndex]:entryPositions[selIndex+1]]
		} else {
			s = haystack[entryPositions[selIndex]:]
		}
		if r.Match(s) {
			updatedSelection = append(updatedSelection, selIndex)
		}
		selIndex++
	}

	return input.WithSelection(updatedSelection)
}

func bool2int(b bool) int {
	// The compiler currently only optimizes this form.
	// See issue 6011.
	var i int
	if b {
		i = 1
	} else {
		i = 0
	}
	return i
}
