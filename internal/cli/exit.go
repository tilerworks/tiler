package cli

import (
	"errors"
	"strings"
)

// usageError marks a command-line usage problem. Flag parse failures
// are wrapped as this type so they exit 2. Runtime failures stay
// plain errors and exit 1.
type usageError struct{ err error }

func (u usageError) Error() string { return u.err.Error() }

func (u usageError) Unwrap() error { return u.err }

// ExitCode maps an execution error to a process exit code. A nil
// error is success. A usage error, including a flag parse failure,
// and an error whose text starts with "unknown command" are exit 2.
// Every other error is exit 1.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var usage usageError
	if errors.As(err, &usage) {
		return 2
	}
	if strings.HasPrefix(err.Error(), "unknown command") {
		return 2
	}
	return 1
}
