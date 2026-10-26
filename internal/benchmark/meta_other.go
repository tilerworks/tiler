//go:build !darwin && !linux

package benchmark

import "runtime"

func goosName() string { return runtime.GOOS }

func diskutilExtras(string) (deviceInfo, error) { return deviceInfo{}, nil }
