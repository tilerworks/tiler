//go:build !linux && !darwin

package doctor

import "io"

func readProgressSize(io.Writer) (int, int) {
	return 0, 0
}
