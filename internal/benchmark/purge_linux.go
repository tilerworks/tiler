//go:build linux

package benchmark

import (
	"os/exec"
)

func platformPurge() string {
	cmd := exec.Command("sudo", "-n", "sh", "-c", "sync; echo 3 > /proc/sys/vm/drop_caches")
	if err := cmd.Run(); err != nil {
		return "no"
	}
	return "yes"
}
