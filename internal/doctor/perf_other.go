//go:build !linux || !(amd64 || arm64 || loong64 || mips64 || mips64le || ppc64 || ppc64le || riscv64 || s390x)

package doctor

import "context"

// measurePerf degrades on platforms (and linux architectures) that
// cannot evict the page cache through the standard fadvise argument
// order: warm-cache numbers would mislead the cache decision, so
// the probe reports itself unavailable instead.
func measurePerf(context.Context, string) (PerfNumbers, error) {
	return PerfNumbers{}, ErrPerfUnavailable
}
