//go:build darwin

package benchmark

import (
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

func goosName() string { return runtime.GOOS }

func writeDiskutilInfo(dev string, w io.Writer) error {
	out, err := exec.Command("diskutil", "info", dev).Output()
	if err != nil {
		return fmt.Errorf("benchmark: diskutil info %s: %w", dev, err)
	}
	wanted := map[string]string{
		"Device Identifier": "device_id",
		"Volume Name":       "volume_name",
		"Mount Point":       "mount_point",
	}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		label, match := wanted[strings.TrimSpace(key)]
		if !match {
			continue
		}
		if _, err := fmt.Fprintf(w, "%s: %s\n", label, strings.TrimSpace(value)); err != nil {
			return fmt.Errorf("benchmark: write metadata: %w", err)
		}
	}
	return nil
}
