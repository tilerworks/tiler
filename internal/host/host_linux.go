//go:build linux

// Package host executes and stats the local machine for doctor.
// This file is the Linux implementation. It does not read the
// mount table.
package host

import (
	"context"
	"os"
	"os/exec"
)

// Available reports that real host probes can be constructed on
// Linux. It does not check whether the FUSE tools are installed.
func Available() error { return nil }

// LookPath resolves a binary on PATH.
func LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

// Stat reports whether a filesystem node is visible. Callers keep
// the returned error as the probe detail.
func Stat(name string) error {
	_, err := os.Stat(name)
	return err
}

// Run executes a binary and returns its combined output. Tools are
// reached only through exec, never through a library binding.
func Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
