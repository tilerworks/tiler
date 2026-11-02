//go:build darwin && arm64

#include "textflag.h"

TEXT libc_readlinkat_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_readlinkat(SB)
GLOBL ·libcReadlinkatAddr(SB), RODATA, $8
DATA ·libcReadlinkatAddr(SB)/8, $libc_readlinkat_trampoline<>(SB)

TEXT libc_renameat_trampoline<>(SB),NOSPLIT,$0-0
	JMP	libc_renameat(SB)
GLOBL ·libcRenameatAddr(SB), RODATA, $8
DATA ·libcRenameatAddr(SB)/8, $libc_renameat_trampoline<>(SB)
