//go:build !darwin && !linux

package benchmark

import (
	"io"
	"runtime"
)

func goosName() string { return runtime.GOOS }

func writeDiskutilInfo(string, io.Writer) error { return nil }
