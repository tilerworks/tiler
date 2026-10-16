//go:build linux && (amd64 || arm64 || loong64 || mips64 || mips64le || ppc64 || ppc64le || riscv64 || s390x)

package doctor

import (
	"context"
	"fmt"
	"os"
	"syscall"
)

// fadvDontNeed is POSIX_FADV_DONTNEED. The value is the same on
// every linux architecture.
const fadvDontNeed = 4

// measurePerf runs the cold-read measurement on linux. The page
// cache eviction uses the fadvise syscall directly, which needs no
// privileges. The stdlib syscall package has no Fadvise wrapper and
// 32-bit linux ABIs pass the arguments differently, so this file is
// restricted to the architectures with the standard argument order;
// every other platform degrades in perf_other.go.
func measurePerf(ctx context.Context, dir string) (PerfNumbers, error) {
	if err := contextErr(ctx, nil); err != nil {
		return PerfNumbers{}, err
	}
	return perfMeasure(dir, perfProbeSize, fadviseEvict)
}

// fadviseEvict asks the kernel to drop the cached pages of one
// region so the next read measures the storage, not the page cache.
// A zero length evicts the whole file from the offset.
func fadviseEvict(f *os.File, offset, length int64) error {
	_, _, errno := syscall.Syscall6(syscall.SYS_FADVISE64,
		f.Fd(), uintptr(offset), uintptr(length), uintptr(fadvDontNeed), 0, 0)
	if errno != 0 {
		return fmt.Errorf("fadvise: %w", errno)
	}
	return nil
}
