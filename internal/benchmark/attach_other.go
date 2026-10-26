//go:build !darwin && !linux

package benchmark

import (
	"fmt"
	"runtime"
)

func platformAttach(Command, Host, string, string, string) (Mount, error) {
	return Mount{}, fmt.Errorf("benchmark: attach is not available on %s", runtime.GOOS)
}

func platformDetach(Command, Mount, string) error {
	return fmt.Errorf("benchmark: detach is not available on %s", runtime.GOOS)
}
