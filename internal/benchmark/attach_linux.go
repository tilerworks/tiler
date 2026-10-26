//go:build linux

package benchmark

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/jiangxin/tiler/internal/container"
)

func platformAttach(cmd Command, host Host, typ, image, point string) (Mount, error) {
	switch typ {
	case "squashfs", "erofs":
		return attachFUSE(cmd, host, typ, image, point)
	default:
		return Mount{}, fmt.Errorf("benchmark: %s attach is not available on linux", typ)
	}
}

func attachFUSE(cmd Command, host Host, typ, image, point string) (Mount, error) {
	argv, err := fuseMountArgv(typ, image, point)
	if err != nil {
		return Mount{}, err
	}
	tool := argv[0]
	if !hostHasTool(host, tool) {
		return attachLoop(cmd, host, typ, image, point, tool)
	}
	if err := runArgv(cmd, argv); err != nil {
		return Mount{}, fmt.Errorf("benchmark: attach %s: %w", typ, err)
	}
	return Mount{Tool: tool}, nil
}

// fuseMountArgv is the read-only image root. subdir and options stay
// empty so the vector matches Format.MountArgv for one whole lower.
func fuseMountArgv(typ, image, point string) ([]string, error) {
	format, err := container.Lookup(typ)
	if err != nil {
		return nil, fmt.Errorf("benchmark: %w", err)
	}
	argv := format.MountArgv(image, "", "", image, point)
	if len(argv) == 0 {
		return nil, fmt.Errorf("benchmark: %s mount argv is empty", typ)
	}
	return argv, nil
}

func hostHasTool(host Host, tool string) bool {
	look := host.LookPath
	if look == nil {
		look = exec.LookPath
	}
	_, err := look(tool)
	return err == nil
}

// attachLoop is the root fallback. A non-root caller hears the
// missing FUSE client and does not try a kernel mount.
func attachLoop(cmd Command, host Host, typ, image, point, tool string) (Mount, error) {
	if hostEuid(host) != 0 {
		return Mount{}, fmt.Errorf(
			"benchmark: missing %s (or run as root for a kernel %s mount)", tool, typ)
	}
	_, err := cmd("mount", nil, "-t", typ, "-o", "ro,loop", image, point)
	if err != nil {
		return Mount{}, fmt.Errorf("benchmark: mount %s: %w", typ, err)
	}
	return Mount{Tool: "mount"}, nil
}

func hostEuid(host Host) int {
	if host.Euid != nil {
		return host.Euid()
	}
	return os.Geteuid()
}

func platformDetach(cmd Command, m Mount, point string) error {
	switch m.Tool {
	case "squashfuse_ll", "erofsfuse":
		return runFuseDetach(cmd, point)
	case "mount":
		return runUmount(cmd, point)
	default:
		return fmt.Errorf("benchmark: cannot detach %q on linux", m.Tool)
	}
}

func runFuseDetach(cmd Command, point string) error {
	_, err := cmd("fusermount3", nil, "-u", point)
	if err != nil {
		return fmt.Errorf("benchmark: fusermount3 %s: %w", point, err)
	}
	return nil
}

func runUmount(cmd Command, point string) error {
	_, err := cmd("umount", nil, point)
	if err != nil {
		return fmt.Errorf("benchmark: umount %s: %w", point, err)
	}
	return nil
}
