package logql

import (
	//"fmt"
	"bytes"

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
	b.selection = selection
	return b
}

func VecFilter(input Batch, col int, needle []byte) Batch {
	vec := input.ColVec(col)
	haystack := vec.Bytes()
	startIndices := input.ColVec(1).Int()
	//endIndices := input.ColVec(2)
	//selection := make([]int, len(startIndices))

	start := int64(0)
	positions := make([]int64, 0)
	//i := 0
	for start < int64(len(haystack)-len(needle)) {
		//i++
		//fmt.Printf("(%d) starting at: %d\n", i, start)
		pos := memmem.Index(haystack[start:], needle)
		if pos == -1 {
			break
		}
		positions = append(positions, start+pos)
		start = start + pos + 1
		//fmt.Printf("(%d) next starting at: %d\n", i, start)
	}

	selection := make([]int, 0)
	pos := positions[0]
	for i := range startIndices {
		// TODO: try to be branchless
		if startIndices[i] <= pos && (i+1 == len(startIndices) || pos < startIndices[i+1]) {
			selection = append(selection, i)
			positions = positions[1:]
			if len(positions) == 0 {
				break
			}
			pos = positions[0]
		}
	}

	return input.WithSelection(selection)
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
