//go:build !linux && !darwin

package checkpoint

import "github.com/jiangxin/tiler/internal/config"

// Restore reports that safe restoration is unavailable on this platform.
func Restore(string, string, config.Config, bool) (string, error) { return "", errUnavailable }

func RestoreWithStatus(string, string, config.Config, bool) (string, bool, error) {
	return "", false, errUnavailable
}
