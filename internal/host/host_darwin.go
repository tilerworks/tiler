//go:build darwin

// Package host executes and stats the local machine for doctor.
// This file is the macOS implementation. It does not mount images.
package host

import (
	"context"
	"os"
	"os/exec"
)

// Available reports that real host probes can be constructed on
// macOS. It does not check whether hdiutil is installed.
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

// SystemHdiutil reports whether hdiutil can be resolved on PATH.
// The check does not execute it.
func SystemHdiutil() error {
	_, err := exec.LookPath("hdiutil")
	return err
}
