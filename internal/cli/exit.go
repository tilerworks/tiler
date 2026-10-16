package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// usageError marks a command-line usage problem. Flag parse failures
// are wrapped as this type so they exit 2. Runtime failures stay
// plain errors and exit 1.
type usageError struct{ err error }

func (u usageError) Error() string { return u.err.Error() }

func (u usageError) Unwrap() error { return u.err }

// maxOneArg and exactOneArg are positional argument validators that
// classify a wrong argument count as a usage error, so it exits 2
// like a flag parse failure instead of looking like a runtime fault.
func maxOneArg(cmd *cobra.Command, args []string) error {
	if len(args) > 1 {
		return usageError{fmt.Errorf("%s accepts at most one argument, received %d",
			cmd.CommandPath(), len(args))}
	}
	return nil
}

func exactOneArg(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return usageError{fmt.Errorf("%s accepts exactly one argument, received %d",
			cmd.CommandPath(), len(args))}
	}
	return nil
}

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
