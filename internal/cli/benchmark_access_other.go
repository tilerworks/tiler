//go:build !unix

package cli

import "errors"

func hostBenchmarkAccess(string) error {
	return errors.New("benchmark: writability check unsupported on this platform")
}
