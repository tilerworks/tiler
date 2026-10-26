package benchmark

import (
	"errors"

	"github.com/jiangxin/tiler/internal/container"
)

// Command runs one external tool. The bytes are combined stdout and
// stderr. Pack and attach adapt Command to container.ToolRunner so
// the container constructors stay the source of each argument vector.
// Tests record the vector and return canned output. A nil Command is
// rejected before any tool starts.
type Command func(name string, env []string, args ...string) ([]byte, error)

func needCommand(cmd Command) error {
	if cmd == nil {
		return errors.New("benchmark: tool runner is nil")
	}
	return nil
}

func toolRunner(cmd Command) container.ToolRunner {
	return func(name string, env []string, args ...string) error {
		_, err := cmd(name, env, args...)
		return err
	}
}

func runArgv(cmd Command, argv []string) error {
	if len(argv) == 0 {
		return errors.New("benchmark: command argv is empty")
	}
	_, err := cmd(argv[0], nil, argv[1:]...)
	return err
}
