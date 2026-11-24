//go:build !linux && !darwin

package checkpoint

import (
	"errors"
	"os"
)

var errUnavailable = errors.New("safe checkpoint directory operations unavailable on this platform")

func openDirectory(string) (*os.File, error) { return nil, errUnavailable }

// Lock reports that safe locking is unavailable on this platform.
func (*Parent) Lock(string) (*os.File, error) { return nil, errUnavailable }

// RenameNoReplace reports that safe renaming is unavailable on this platform.
func (*Parent) RenameNoReplace(string, string) error { return errUnavailable }

// Exchange reports that atomic exchange is unavailable on this platform.
func (*Parent) Exchange(string, string) error { return errUnavailable }
