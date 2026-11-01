//go:build linux

package cli

import "os/exec"

func hostConcatAvailable() bool {
	_, err := exec.LookPath("concatfs")
	return err == nil
}
