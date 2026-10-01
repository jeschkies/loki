//go:build amd64

package log

import "golang.org/x/sys/cpu"

// matchLoAVX2 compares los[0 : n8*8] against the broadcast target, 8
// lanes per iteration (two AVX2 VPCMPEQQ over 4 lanes each), writing
// one bit per element into mask[0:n8] - overwriting those bytes
// completely, not OR-ing (callers relying on pre-existing bits in that
// range must save them first). Requires AVX2. Implemented in
// matchlo_amd64.s.
//
//go:noescape
func matchLoAVX2(los *uint64, n8 int, target uint64, mask *byte)

func init() {
	if cpu.X86.HasAVX2 {
		matchLo = func(los []uint64, target uint64, mask []byte) {
			n8 := len(los) / 8
			if n8 > 0 {
				matchLoAVX2(&los[0], n8, target, &mask[0])
			}
			for i := n8 * 8; i < len(los); i++ {
				if los[i] == target {
					mask[i/8] |= 1 << (i % 8)
				}
			}
		}
	}
}
