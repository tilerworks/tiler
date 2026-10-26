//go:build darwin

package benchmark

import (
	"os/exec"
)

func platformPurge() string {
	cmd := exec.Command("sudo", "-n", "purge")
	if err := cmd.Run(); err != nil {
		return "no"
	}
	return "yes"
}
