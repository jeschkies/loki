#include "textflag.h"

// func matchLoAVX2(los *uint64, n8 int, target uint64, mask *byte)
// Requires: AVX, AVX2
TEXT ·matchLoAVX2(SB), NOSPLIT, $0-32
	MOVQ los+0(FP), SI
	MOVQ n8+8(FP), CX
	MOVQ target+16(FP), AX
	MOVQ mask+24(FP), DI

	TESTQ CX, CX
	JZ    done

	MOVQ         AX, X0
	VPBROADCASTQ X0, Y0

loop:
	VMOVDQU   (SI), Y1
	VMOVDQU   32(SI), Y2
	VPCMPEQQ  Y0, Y1, Y1
	VPCMPEQQ  Y0, Y2, Y2
	VMOVMSKPD Y1, AX
	VMOVMSKPD Y2, DX
	SHLL      $4, DX
	ORL       DX, AX
	MOVB      AL, (DI)

	ADDQ $64, SI
	INCQ DI
	DECQ CX
	JNZ  loop

done:
	VZEROUPPER
	RET
