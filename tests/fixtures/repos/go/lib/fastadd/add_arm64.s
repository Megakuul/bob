#include "textflag.h"

// func add(a, b int64) int64
TEXT ·add(SB), NOSPLIT, $0-24
	MOVD a+0(FP), R0
	MOVD b+8(FP), R1
	ADD  R1, R0, R0
	MOVD R0, ret+16(FP)
	RET
