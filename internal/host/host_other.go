//go:build !linux

// Package host executes and stats the local machine for doctor.
// This file keeps non-Linux builds compiling by returning a clear
// error from every seam.
package host

import (
	"context"
	"errors"
)

// errNotLinux is returned by every host seam on non-Linux systems
// so the package still builds under go test ./...
var errNotLinux = errors.New("host probes require linux")

// Available reports that default seams cannot be constructed here.
func Available() error { return errNotLinux }

// LookPath reports that host probes require Linux.
func LookPath(string) (string, error) { return "", errNotLinux }

// Stat reports that host probes require Linux.
func Stat(string) error { return errNotLinux }

// Run reports that host probes require Linux.
func Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errNotLinux
}
