//+build !noasm !appengine

// ARROW-15336
// (C2GOASM doesn't work correctly for Arm64)
// Partly GENERATED BY asm2plan9s.


// func _int32_max_min_neon(values unsafe.Pointer, length int, minout, maxout unsafe.Pointer)
TEXT ·_int32_max_min_neon(SB), $0-32

	MOVD    values+0(FP), R0
	MOVD    length+8(FP), R1
	MOVD    minout+16(FP), R2
	MOVD    maxout+24(FP), R3

	// The Go ABI saves the frame pointer register one word below the 
	// caller's frame. Make room so we don't overwrite it. Needs to stay 
	// 16-byte aligned 
	SUB $16, RSP
	WORD $0xa9bf7bfd // stp x29, x30, [sp, #-16]!
	WORD $0x7100043f // cmp    w1, #1
	WORD $0x910003fd // mov    x29, sp
	BLT LBB0_3

	WORD $0x71000c3f // cmp    w1, #3
	WORD $0x2a0103e8 // mov    w8, w1
	BHI LBB0_4

	WORD $0xaa1f03e9 // mov    x9, xzr
	WORD $0x52b0000b // mov    w11, #-2147483648
	WORD $0x12b0000a // mov    w10, #2147483647
	JMP LBB0_7
LBB0_3:
	WORD $0x12b0000a // mov    w10, #2147483647
	WORD $0x52b0000b // mov    w11, #-2147483648
	WORD $0xb900006b // str    w11, [x3]
	WORD $0xb900004a // str    w10, [x2]
	WORD $0xa8c17bfd // ldp    x29, x30, [sp], #16
	// Put the stack pointer back where it was 
	ADD $16, RSP
	RET
LBB0_4:
	WORD $0x927e7509 // and    x9, x8, #0xfffffffc
	WORD $0x9100200a // add    x10, x0, #8
	WORD $0x0f046402 // movi    v2.2s, #128, lsl #24
	WORD $0x2f046400 // mvni    v0.2s, #128, lsl #24
	WORD $0x2f046401 // mvni    v1.2s, #128, lsl #24
	WORD $0xaa0903eb // mov    x11, x9
	WORD $0x0f046403 // movi    v3.2s, #128, lsl #24
LBB0_5:
	WORD $0x6d7f9544 // ldp    d4, d5, [x10, #-8]
	WORD $0xf100116b // subs    x11, x11, #4
	WORD $0x9100414a // add    x10, x10, #16
	WORD $0x0ea46c00 // smin    v0.2s, v0.2s, v4.2s
	WORD $0x0ea56c21 // smin    v1.2s, v1.2s, v5.2s
	WORD $0x0ea46442 // smax    v2.2s, v2.2s, v4.2s
	WORD $0x0ea56463 // smax    v3.2s, v3.2s, v5.2s
	BNE LBB0_5

	WORD $0x0ea36442 // smax    v2.2s, v2.2s, v3.2s
	WORD $0x0ea16c00 // smin    v0.2s, v0.2s, v1.2s
	WORD $0x0e0c0441 // dup    v1.2s, v2.s[1]
	WORD $0x0e0c0403 // dup    v3.2s, v0.s[1]
	WORD $0x0ea16441 // smax    v1.2s, v2.2s, v1.2s
	WORD $0x0ea36c00 // smin    v0.2s, v0.2s, v3.2s
	WORD $0xeb08013f // cmp    x9, x8
	WORD $0x1e26002b // fmov    w11, s1
	WORD $0x1e26000a // fmov    w10, s0
	BEQ LBB0_9
LBB0_7:
	WORD $0x8b09080c // add    x12, x0, x9, lsl #2
	WORD $0xcb090108 // sub    x8, x8, x9
LBB0_8:
	WORD $0xb8404589 // ldr    w9, [x12], #4
	WORD $0x6b09015f // cmp    w10, w9
	WORD $0x1a89b14a // csel    w10, w10, w9, lt
	WORD $0x6b09017f // cmp    w11, w9
	WORD $0x1a89c16b // csel    w11, w11, w9, gt
	WORD $0xf1000508 // subs    x8, x8, #1
	BNE LBB0_8
LBB0_9:
	WORD $0xb900006b // str    w11, [x3]
	WORD $0xb900004a // str    w10, [x2]
	WORD $0xa8c17bfd // ldp    x29, x30, [sp], #16
	// Put the stack pointer back where it was 
	ADD $16, RSP
	RET

// func _uint32_max_min_neon(values unsafe.Pointer, length int, minout, maxout unsafe.Pointer)
TEXT ·_uint32_max_min_neon(SB), $0-32

	MOVD    values+0(FP), R0
	MOVD    length+8(FP), R1
	MOVD    minout+16(FP), R2
	MOVD    maxout+24(FP), R3
    
	// The Go ABI saves the frame pointer register one word below the 
	// caller's frame. Make room so we don't overwrite it. Needs to stay 
	// 16-byte aligned 
	SUB $16, RSP
	WORD $0xa9bf7bfd // stp x29, x30, [sp, #-16]!
	WORD $0x7100043f // cmp    w1, #1
	WORD $0x910003fd // mov    x29, sp
	BLT LBB1_3

	WORD $0x71000c3f // cmp    w1, #3
	WORD $0x2a0103e8 // mov    w8, w1
	BHI LBB1_4

	WORD $0xaa1f03e9 // mov    x9, xzr
	WORD $0x2a1f03ea // mov    w10, wzr
	WORD $0x1280000b // mov    w11, #-1
	JMP LBB1_7
LBB1_3:
	WORD $0x2a1f03ea // mov    w10, wzr
	WORD $0x1280000b // mov    w11, #-1
	WORD $0xb900006a // str    w10, [x3]
	WORD $0xb900004b // str    w11, [x2]
	WORD $0xa8c17bfd // ldp    x29, x30, [sp], #16
	// Put the stack pointer back where it was 
	ADD $16, RSP
	RET
LBB1_4:
	WORD $0x927e7509 // and    x9, x8, #0xfffffffc
	WORD $0x6f00e401 // movi    v1.2d, #0000000000000000
	WORD $0x6f07e7e0 // movi    v0.2d, #0xffffffffffffffff
	WORD $0x9100200a // add    x10, x0, #8
	WORD $0x6f07e7e2 // movi    v2.2d, #0xffffffffffffffff
	WORD $0xaa0903eb // mov    x11, x9
	WORD $0x6f00e403 // movi    v3.2d, #0000000000000000
LBB1_5:
	WORD $0x6d7f9544 // ldp    d4, d5, [x10, #-8]
	WORD $0xf100116b // subs    x11, x11, #4
	WORD $0x9100414a // add    x10, x10, #16
	WORD $0x2ea46c00 // umin    v0.2s, v0.2s, v4.2s
	WORD $0x2ea56c42 // umin    v2.2s, v2.2s, v5.2s
	WORD $0x2ea46421 // umax    v1.2s, v1.2s, v4.2s
	WORD $0x2ea56463 // umax    v3.2s, v3.2s, v5.2s
	BNE LBB1_5

	WORD $0x2ea36421 // umax    v1.2s, v1.2s, v3.2s
	WORD $0x2ea26c00 // umin    v0.2s, v0.2s, v2.2s
	WORD $0x0e0c0422 // dup    v2.2s, v1.s[1]
	WORD $0x0e0c0403 // dup    v3.2s, v0.s[1]
	WORD $0x2ea26421 // umax    v1.2s, v1.2s, v2.2s
	WORD $0x2ea36c00 // umin    v0.2s, v0.2s, v3.2s
	WORD $0xeb08013f // cmp    x9, x8
	WORD $0x1e26002a // fmov    w10, s1
	WORD $0x1e26000b // fmov    w11, s0
	BEQ LBB1_9
LBB1_7:
	WORD $0x8b09080c // add    x12, x0, x9, lsl #2
	WORD $0xcb090108 // sub    x8, x8, x9
LBB1_8:
	WORD $0xb8404589 // ldr    w9, [x12], #4
	WORD $0x6b09017f // cmp    w11, w9
	WORD $0x1a89316b // csel    w11, w11, w9, lo
	WORD $0x6b09015f // cmp    w10, w9
	WORD $0x1a89814a // csel    w10, w10, w9, hi
	WORD $0xf1000508 // subs    x8, x8, #1
	BNE LBB1_8
LBB1_9:
	WORD $0xb900006a // str    w10, [x3]
	WORD $0xb900004b // str    w11, [x2]
	WORD $0xa8c17bfd // ldp    x29, x30, [sp], #16
	// Put the stack pointer back where it was 
	ADD $16, RSP
	RET

// func _int64_max_min_neon(values unsafe.Pointer, length int, minout, maxout unsafe.Pointer)
TEXT ·_int64_max_min_neon(SB), $0-32

        MOVD    values+0(FP), R0
        MOVD    length+8(FP), R1
        MOVD    minout+16(FP), R2
        MOVD    maxout+24(FP), R3

	// The Go ABI saves the frame pointer register one word below the 
	// caller's frame. Make room so we don't overwrite it. Needs to stay 
	// 16-byte aligned 
	SUB $16, RSP
	WORD $0xa9bf7bfd // stp    x29, x30, [sp, #-16]!
	WORD $0x7100043f // cmp    w1, #1
	WORD $0x910003fd // mov    x29, sp
	BLT LBB2_3

	WORD $0x2a0103e8 // mov    w8, w1
	WORD $0xd2f0000b // mov    x11, #-9223372036854775808
	WORD $0x71000c3f // cmp    w1, #3
	WORD $0x92f0000a // mov    x10, #9223372036854775807
	BHI LBB2_4

	WORD $0xaa1f03e9 // mov    x9, xzr
	JMP LBB2_7
LBB2_3:
	WORD $0x92f0000a // mov    x10, #9223372036854775807
	WORD $0xd2f0000b // mov    x11, #-9223372036854775808
	WORD $0xf900006b // str    x11, [x3]
	WORD $0xf900004a // str    x10, [x2]
	WORD $0xa8c17bfd // ldp    x29, x30, [sp], #16
	// Put the stack pointer back where it was 
	ADD $16, RSP
	RET
LBB2_4:
	WORD $0x927e7509 // and    x9, x8, #0xfffffffc
	WORD $0x4e080d61 // dup    v1.2d, x11
	WORD $0x4e080d40 // dup    v0.2d, x10
	WORD $0x9100400a // add    x10, x0, #16
	WORD $0xaa0903eb // mov    x11, x9
	WORD $0x4ea01c02 // mov    v2.16b, v0.16b
	WORD $0x4ea11c23 // mov    v3.16b, v1.16b
LBB2_5:
	WORD $0xad7f9544 // ldp    q4, q5, [x10, #-16]
	WORD $0x4ea31c66 // mov    v6.16b, v3.16b
	WORD $0x4ea11c27 // mov    v7.16b, v1.16b
	WORD $0x4ea21c43 // mov    v3.16b, v2.16b
	WORD $0x4ea01c01 // mov    v1.16b, v0.16b
	WORD $0x4ee03480 // cmgt    v0.2d, v4.2d, v0.2d
	WORD $0x4ee234a2 // cmgt    v2.2d, v5.2d, v2.2d
	WORD $0x6e641c20 // bsl    v0.16b, v1.16b, v4.16b
	WORD $0x4ee434e1 // cmgt    v1.2d, v7.2d, v4.2d
	WORD $0x6e651c62 // bsl    v2.16b, v3.16b, v5.16b
	WORD $0x4ee534c3 // cmgt    v3.2d, v6.2d, v5.2d
	WORD $0xf100116b // subs    x11, x11, #4
	WORD $0x6e641ce1 // bsl    v1.16b, v7.16b, v4.16b
	WORD $0x6e651cc3 // bsl    v3.16b, v6.16b, v5.16b
	WORD $0x9100814a // add    x10, x10, #32
	BNE LBB2_5

	WORD $0x4ee33424 // cmgt    v4.2d, v1.2d, v3.2d
	WORD $0x4ee03445 // cmgt    v5.2d, v2.2d, v0.2d
	WORD $0x6e631c24 // bsl    v4.16b, v1.16b, v3.16b
	WORD $0x6e621c05 // bsl    v5.16b, v0.16b, v2.16b
	WORD $0x4e180480 // dup    v0.2d, v4.d[1]
	WORD $0x4e1804a1 // dup    v1.2d, v5.d[1]
	WORD $0x4ee03482 // cmgt    v2.2d, v4.2d, v0.2d
	WORD $0x4ee53423 // cmgt    v3.2d, v1.2d, v5.2d
	WORD $0x6e601c82 // bsl    v2.16b, v4.16b, v0.16b
	WORD $0x6e611ca3 // bsl    v3.16b, v5.16b, v1.16b
	WORD $0xeb08013f // cmp    x9, x8
	WORD $0x9e66004b // fmov    x11, d2
	WORD $0x9e66006a // fmov    x10, d3
	BEQ LBB2_9
LBB2_7:
	WORD $0x8b090c0c // add    x12, x0, x9, lsl #3
	WORD $0xcb090108 // sub    x8, x8, x9
LBB2_8:
	WORD $0xf8408589 // ldr    x9, [x12], #8
	WORD $0xeb09015f // cmp    x10, x9
	WORD $0x9a89b14a // csel    x10, x10, x9, lt
	WORD $0xeb09017f // cmp    x11, x9
	WORD $0x9a89c16b // csel    x11, x11, x9, gt
	WORD $0xf1000508 // subs    x8, x8, #1
	BNE LBB2_8
LBB2_9:
	WORD $0xf900006b // str    x11, [x3]
	WORD $0xf900004a // str    x10, [x2]
	WORD $0xa8c17bfd // ldp    x29, x30, [sp], #16
	// Put the stack pointer back where it was 
	ADD $16, RSP
	RET


// func _uint64_max_min_neon(values unsafe.Pointer, length int, minout, maxout unsafe.Pointer)
TEXT ·_uint64_max_min_neon(SB), $0-32

        MOVD    values+0(FP), R0
        MOVD    length+8(FP), R1
        MOVD    minout+16(FP), R2
        MOVD    maxout+24(FP), R3

	// The Go ABI saves the frame pointer register one word below the 
	// caller's frame. Make room so we don't overwrite it. Needs to stay 
	// 16-byte aligned 
	SUB $16, RSP
	WORD $0xa9bf7bfd // stp    x29, x30, [sp, #-16]!
	WORD $0x7100043f // cmp    w1, #1
	WORD $0x910003fd // mov    x29, sp
	BLT LBB3_3

	WORD $0x71000c3f // cmp    w1, #3
	WORD $0x2a0103e8 // mov    w8, w1
	BHI LBB3_4

	WORD $0xaa1f03e9 // mov    x9, xzr
	WORD $0xaa1f03ea // mov    x10, xzr
	WORD $0x9280000b // mov    x11, #-1
	JMP LBB3_7
LBB3_3:
	WORD $0xaa1f03ea // mov    x10, xzr
	WORD $0x9280000b // mov    x11, #-1
	WORD $0xf900006a // str    x10, [x3]
	WORD $0xf900004b // str    x11, [x2]
	WORD $0xa8c17bfd // ldp    x29, x30, [sp], #16
	// Put the stack pointer back where it was 
	ADD $16, RSP
	RET
LBB3_4:
	WORD $0x927e7509 // and    x9, x8, #0xfffffffc
	WORD $0x9100400a // add    x10, x0, #16
	WORD $0x6f00e401 // movi    v1.2d, #0000000000000000
	WORD $0x6f07e7e0 // movi    v0.2d, #0xffffffffffffffff
	WORD $0x6f07e7e2 // movi    v2.2d, #0xffffffffffffffff
	WORD $0xaa0903eb // mov    x11, x9
	WORD $0x6f00e403 // movi    v3.2d, #0000000000000000
LBB3_5:
	WORD $0xad7f9544 // ldp    q4, q5, [x10, #-16]
	WORD $0x4ea31c66 // mov    v6.16b, v3.16b
	WORD $0x4ea11c27 // mov    v7.16b, v1.16b
	WORD $0x4ea21c43 // mov    v3.16b, v2.16b
	WORD $0x4ea01c01 // mov    v1.16b, v0.16b
	WORD $0x6ee03480 // cmhi    v0.2d, v4.2d, v0.2d
	WORD $0x6ee234a2 // cmhi    v2.2d, v5.2d, v2.2d
	WORD $0x6e641c20 // bsl    v0.16b, v1.16b, v4.16b
	WORD $0x6ee434e1 // cmhi    v1.2d, v7.2d, v4.2d
	WORD $0x6e651c62 // bsl    v2.16b, v3.16b, v5.16b
	WORD $0x6ee534c3 // cmhi    v3.2d, v6.2d, v5.2d
	WORD $0xf100116b // subs    x11, x11, #4
	WORD $0x6e641ce1 // bsl    v1.16b, v7.16b, v4.16b
	WORD $0x6e651cc3 // bsl    v3.16b, v6.16b, v5.16b
	WORD $0x9100814a // add    x10, x10, #32
	BNE LBB3_5

	WORD $0x6ee33424 // cmhi    v4.2d, v1.2d, v3.2d
	WORD $0x6ee03445 // cmhi    v5.2d, v2.2d, v0.2d
	WORD $0x6e631c24 // bsl    v4.16b, v1.16b, v3.16b
	WORD $0x6e621c05 // bsl    v5.16b, v0.16b, v2.16b
	WORD $0x4e180480 // dup    v0.2d, v4.d[1]
	WORD $0x4e1804a1 // dup    v1.2d, v5.d[1]
	WORD $0x6ee03482 // cmhi    v2.2d, v4.2d, v0.2d
	WORD $0x6ee53423 // cmhi    v3.2d, v1.2d, v5.2d
	WORD $0x6e601c82 // bsl    v2.16b, v4.16b, v0.16b
	WORD $0x6e611ca3 // bsl    v3.16b, v5.16b, v1.16b
	WORD $0xeb08013f // cmp    x9, x8
	WORD $0x9e66004a // fmov    x10, d2
	WORD $0x9e66006b // fmov    x11, d3
	BEQ LBB3_9
LBB3_7:
	WORD $0x8b090c0c // add    x12, x0, x9, lsl #3
	WORD $0xcb090108 // sub    x8, x8, x9
LBB3_8:
	WORD $0xf8408589 // ldr    x9, [x12], #8
	WORD $0xeb09017f // cmp    x11, x9
	WORD $0x9a89316b // csel    x11, x11, x9, lo
	WORD $0xeb09015f // cmp    x10, x9
	WORD $0x9a89814a // csel    x10, x10, x9, hi
	WORD $0xf1000508 // subs    x8, x8, #1
	BNE LBB3_8
LBB3_9:
	WORD $0xf900006a // str    x10, [x3]
	WORD $0xf900004b // str    x11, [x2]
	WORD $0xa8c17bfd // ldp    x29, x30, [sp], #16
	// Put the stack pointer back where it was 
	ADD $16, RSP
	RET

