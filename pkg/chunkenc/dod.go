package chunkenc

// bitWriter is a minimal hand-rolled bit-packing writer: it appends an
// arbitrary number of bits (1-64) at a time to a byte slice, tracking how
// many bits are free in the last byte. Used for delta-of-delta timestamp
// encoding below.
//
// This is a standalone reimplementation of the same general approach as
// Prometheus's tsdb/chunkenc/bstream.go, not a reuse of it: bstream is
// unexported (package-private to Prometheus's own chunkenc package) and,
// more importantly, its call sites (xorAppender) interleave the
// delta-of-delta bit writes with float-value XOR bit writes we have no use
// for. Hand-rolling just the bit-packing primitive keeps this self-
// contained and avoids pulling in that unrelated logic.
type bitWriter struct {
	buf  []byte
	free uint8 // free bits in the last byte of buf; 0 means buf is empty or its last byte is full.
}

func (w *bitWriter) writeBit(b bool) {
	if w.free == 0 {
		w.buf = append(w.buf, 0)
		w.free = 8
	}
	if b {
		w.buf[len(w.buf)-1] |= 1 << (w.free - 1)
	}
	w.free--
}

// writeBits writes the nbits low-order bits of u, most significant bit
// first. nbits must be between 0 and 64.
func (w *bitWriter) writeBits(u uint64, nbits int) {
	for nbits > 0 {
		if w.free == 0 {
			w.buf = append(w.buf, 0)
			w.free = 8
		}
		n := nbits
		if n > int(w.free) {
			n = int(w.free)
		}
		shift := nbits - n
		bits := byte(u>>uint(shift)) & byte(1<<n-1)
		w.buf[len(w.buf)-1] |= bits << (w.free - uint8(n))
		w.free -= uint8(n)
		nbits -= n
	}
}

// bitReader mirrors bitWriter for reading. bitPos is the total number of
// bits consumed so far; bitPos>>3 is the current byte index and bitPos&7
// the bit index within it, counted from the most significant bit.
type bitReader struct {
	buf    []byte
	bitPos int
}

func (r *bitReader) readBit() bool {
	byteIdx := r.bitPos >> 3
	bitIdx := 7 - uint(r.bitPos&7)
	r.bitPos++
	return (r.buf[byteIdx]>>bitIdx)&1 != 0
}

// readBits reads the next nbits bits (0-64) and returns them right-aligned
// in a uint64.
func (r *bitReader) readBits(nbits int) uint64 {
	var v uint64
	for nbits > 0 {
		byteIdx := r.bitPos >> 3
		bitIdx := uint(r.bitPos & 7)
		avail := int(8 - bitIdx)
		take := nbits
		if take > avail {
			take = avail
		}
		shift := avail - take
		mask := byte(1<<take - 1)
		bits := (r.buf[byteIdx] >> uint(shift)) & mask
		v = v<<uint(take) | uint64(bits)
		r.bitPos += take
		nbits -= take
	}
	return v
}

// signExtend interprets the low nbits bits of v as a two's-complement
// signed integer and sign-extends it to a full int64.
func signExtend(v uint64, nbits int) int64 {
	shift := 64 - nbits
	return int64(v<<uint(shift)) >> uint(shift)
}

// dodBitRange reports whether x fits in the top nbits of a two's-complement
// representation, matching Prometheus's tsdb/chunkenc.bitRange (same
// Gorilla-paper size classes; reimplemented here since that one is
// unexported).
func dodBitRange(x int64, nbits uint8) bool {
	return -((int64(1)<<(nbits-1))-1) <= x && x <= int64(1)<<(nbits-1)
}

// encodeTimestampsDoD hand-rolled-delta-of-delta encodes ts: the first
// value is stored in full, the second as a plain 64-bit delta from the
// first, and every value after that as the delta of successive deltas
// (delta-of-delta), using the same variable-width bit size classes as the
// Gorilla paper / Prometheus's XOR chunk (0 bits if unchanged, else a short
// control prefix followed by 14, 17, 20, or 64 value bits). This is the
// same technique, reimplemented standalone - see bitWriter's doc comment
// for why.
func encodeTimestampsDoD(ts []int64) []byte {
	w := &bitWriter{buf: make([]byte, 0, len(ts)*2+16)}
	if len(ts) == 0 {
		return w.buf
	}

	w.writeBits(uint64(ts[0]), 64)
	if len(ts) == 1 {
		return w.buf
	}

	prevDelta := ts[1] - ts[0]
	w.writeBits(uint64(prevDelta), 64)
	prev := ts[1]

	for i := 2; i < len(ts); i++ {
		delta := ts[i] - prev
		dod := delta - prevDelta
		writeDoD(w, dod)
		prev = ts[i]
		prevDelta = delta
	}
	return w.buf
}

func writeDoD(w *bitWriter, dod int64) {
	switch {
	case dod == 0:
		w.writeBit(false)
	case dodBitRange(dod, 14):
		w.writeBits(0b10, 2)
		w.writeBits(uint64(dod)&(1<<14-1), 14)
	case dodBitRange(dod, 17):
		w.writeBits(0b110, 3)
		w.writeBits(uint64(dod)&(1<<17-1), 17)
	case dodBitRange(dod, 20):
		w.writeBits(0b1110, 4)
		w.writeBits(uint64(dod)&(1<<20-1), 20)
	default:
		w.writeBits(0b1111, 4)
		w.writeBits(uint64(dod), 64)
	}
}

// decodeTimestampsDoD decodes n timestamps previously written by
// encodeTimestampsDoD, appending them to dst (which is grown/reused if it
// has enough capacity) and returning the result.
func decodeTimestampsDoD(data []byte, n int, dst []int64) []int64 {
	if n == 0 {
		return []int64{}
	}
	if cap(dst) < n {
		dst = make([]int64, n)
	} else {
		dst = dst[:n]
	}

	r := &bitReader{buf: data}
	dst[0] = int64(r.readBits(64))
	if n == 1 {
		return dst
	}

	delta := int64(r.readBits(64))
	dst[1] = dst[0] + delta

	for i := 2; i < n; i++ {
		dod := readDoD(r)
		delta += dod
		dst[i] = dst[i-1] + delta
	}
	return dst
}

func readDoD(r *bitReader) int64 {
	if !r.readBit() {
		return 0
	}
	if !r.readBit() {
		return signExtend(r.readBits(14), 14)
	}
	if !r.readBit() {
		return signExtend(r.readBits(17), 17)
	}
	if !r.readBit() {
		return signExtend(r.readBits(20), 20)
	}
	return int64(r.readBits(64))
}
