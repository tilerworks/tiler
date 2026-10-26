package benchmark

import "errors"

// Host is the process seam for a Linux read-only attach.
// LookPath reports whether the FUSE client is installed.
// Euid is the effective uid. Nil fields use the host process.
// Darwin attach does not consult Host.
type Host struct {
	LookPath func(name string) (string, error)
	Euid     func() int
}

// Mount is one successful read-only attach. Tool selects detach:
// hdiutil, squashfuse_ll, erofsfuse, or mount for a kernel loop.
type Mount struct {
	Tool string
}

// Attach mounts image read-only at point. tar returns an error and
// does not call cmd. Squashfs and erofs prefer the FUSE client from
// MountArgv. A missing client uses a root loop mount.
func Attach(cmd Command, host Host, typ, image, point string) (Mount, error) {
	if typ == "tar" {
		return Mount{}, errors.New("benchmark: tar is not mounted")
	}
	if err := needCommand(cmd); err != nil {
		return Mount{}, err
	}
	return platformAttach(cmd, host, typ, image, point)
}

// Detach releases a mount returned by Attach. A dmg detach that
// fails falls back to diskutil eject.
func Detach(cmd Command, m Mount, point string) error {
	if err := needCommand(cmd); err != nil {
		return err
	}
	return platformDetach(cmd, m, point)
}
