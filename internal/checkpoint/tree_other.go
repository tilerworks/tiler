//go:build !linux && !darwin

package checkpoint

import "os"

// CopyTree reports that safe tree copying is unavailable on this platform.
func CopyTree(*os.File, *os.File) error { return errUnavailable }
