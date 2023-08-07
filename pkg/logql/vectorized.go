package logql

import (
	//"fmt"

	memmem "github.com/jeschkies/go-memmem/pkg/search"
)

type Vec interface {
	Bytes() []byte
}

type bytesVec []byte

func (b bytesVec) Bytes() []byte {
	return b
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
	//startIndices := input.ColVec(1)
	//endIndices := input.ColVec(2)
	//selection := make([]int, len(startIndices))

	start := int64(0)
	//i := 0
	for start < int64(len(haystack)-len(needle)) {
		//i++
		//fmt.Printf("(%d) starting at: %d\n", i, start)
		index := memmem.Index(haystack[start:], needle)
		if index == -1 {
			break
		}
		start = start + index + 1
		//fmt.Printf("(%d) next starting at: %d\n", i, start)
	}

	return input
}
