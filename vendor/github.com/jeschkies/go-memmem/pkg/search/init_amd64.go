package search

import (
	"bytes"

	"golang.org/x/sys/cpu"
)

func init() {
	if cpu.X86.HasAVX2 {
		index = indexAvx2
	} else {
		index = func(haystack []byte, needle []byte) int64 { return int64(bytes.Index(haystack, needle)) }
	}
}

