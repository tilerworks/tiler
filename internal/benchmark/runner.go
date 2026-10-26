package benchmark

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/container"
)

const detachLogName = "detach.log"

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

// logCommand appends combined tool output under dir. A nil command,
// or an empty dir or file, is returned unchanged. A log write
// failure does not replace the tool error.
func logCommand(cmd Command, dir, file string) Command {
	if cmd == nil || dir == "" || file == "" {
		return cmd
	}
	path := filepath.Join(dir, file)
	return func(name string, env []string, args ...string) ([]byte, error) {
		out, err := cmd(name, env, args...)
		appendToolLog(path, out, err)
		return out, err
	}
}

func appendToolLog(path string, out []byte, runErr error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if len(out) > 0 {
		_, _ = f.Write(out)
	}
	if runErr == nil {
		return
	}
	if len(out) == 0 || out[len(out)-1] != '\n' {
		_, _ = f.Write([]byte{'\n'})
	}
	_, _ = fmt.Fprintf(f, "benchmark: %s\n", runErr.Error())
}

func packLogName(typ string) string {
	switch typ {
	case "tar":
		return "tar-create.log"
	case "dmg":
		return "hdiutil-create.log"
	case "squashfs":
		return "mksquashfs.log"
	case "erofs":
		return "mkfs-erofs.log"
	default:
		return typ + ".log"
	}
}

func attachLogName(typ string) string {
	switch typ {
	case "dmg":
		return "hdiutil-attach.log"
	default:
		return typ + "-attach.log"
	}
}
