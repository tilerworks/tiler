//go:build darwin

package benchmark

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

func goosName() string { return runtime.GOOS }

func diskutilExtras(dev string) (deviceInfo, error) {
	out, err := exec.Command("diskutil", "info", dev).Output()
	if err != nil {
		return deviceInfo{}, fmt.Errorf("benchmark: diskutil info %s: %w", dev, err)
	}
	wanted := map[string]string{
		"Device Identifier": "device_id",
		"Volume Name":       "volume_name",
		"Mount Point":       "mount_point",
	}
	var info deviceInfo
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		label, match := wanted[strings.TrimSpace(key)]
		if !match {
			continue
		}
		value = strings.TrimSpace(value)
		switch label {
		case "device_id":
			info.DeviceID = value
		case "volume_name":
			info.VolumeName = value
		case "mount_point":
			info.MountPoint = value
		}
	}
	return info, nil
}
