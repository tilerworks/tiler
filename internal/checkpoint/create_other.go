//go:build !linux && !darwin

package checkpoint

import "github.com/jiangxin/tiler/internal/config"

// Create reports that safe checkpoint creation is unavailable on this platform.
func Create(string, string, config.Config) (string, error) { return "", errUnavailable }

func CreateWithStatus(string, string, config.Config) (string, bool, error) {
	return "", false, errUnavailable
}
