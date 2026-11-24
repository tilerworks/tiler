//go:build !darwin

package checkpoint

import "github.com/jiangxin/tiler/internal/config"

func checkpointPathSpelling(path string, _ bool) (string, error) { return path, nil }

func checkPlatformProtectedPaths(string, string, config.Config) error { return nil }
