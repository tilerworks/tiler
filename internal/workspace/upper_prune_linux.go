//go:build linux

package workspace

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// overlayXattrPrefix is the xattr namespace where overlay filesystems
// store their markers. Reading or writing it may need privilege;
// failures surface as errors and the prune rules keep entries
// fail-safe.
const overlayXattrPrefix = "trusted.overlay."

// platformStatOf fills the raw stat fields from an already-Lstat'd
// FileInfo, the same way devIno and fileUID read the *syscall.Stat_t
// behind Sys(). Deriving the fields from the walk's own stat keeps
// every field of one rule decision describing the same inode and
// costs no second syscall.
func platformStatOf(info os.FileInfo) (platformStat, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return platformStat{}, errors.New("workspace: upper prune: cannot read raw stat fields")
	}
	return platformStat{
		rdev:  uint64(st.Rdev),
		nlink: uint64(st.Nlink),
		uid:   st.Uid,
		gid:   st.Gid,
	}, nil
}

// overlayXattrs includes trusted overlay markers and FUSE opacity. The
// FUSE marker file is normalized to the same key as its optional xattr.
func overlayXattrs(path string) (map[string][]byte, error) {
	names, err := listXattr(path)
	if err != nil {
		return nil, err
	}
	found := make(map[string][]byte, len(names))
	for _, name := range names {
		if !strings.HasPrefix(name, overlayXattrPrefix) && name != xattrFuseOpaque {
			continue
		}
		value, err := getXattr(path, name)
		if errors.Is(err, syscall.ENODATA) {
			continue // gone between listing and reading
		}
		if err != nil {
			return nil, err
		}
		found[name] = value
	}
	_, err = os.Lstat(filepath.Join(path, ".wh..wh..opq"))
	if err == nil {
		found[xattrFuseOpaque] = []byte("y")
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return nil, err
	}
	return found, nil
}

// stripOverlayXattr removes one xattr from path without following
// symlinks, so a link swapped in mid-walk cannot redirect the
// removal to a target outside the pruned tree.
func stripOverlayXattr(path, name string) error {
	return lremovexattr(path, name)
}

// listXattr returns every xattr name on path without following
// symlinks, growing the buffer until it fits.
func listXattr(path string) ([]string, error) {
	for size := 512; ; size *= 2 {
		buf := make([]byte, size)
		n, err := llistxattr(path, buf)
		if errors.Is(err, syscall.ERANGE) {
			continue
		}
		if err != nil {
			if isNoXattrErr(err) {
				return nil, nil
			}
			return nil, err
		}
		return splitXattrNames(buf[:n]), nil
	}
}

// getXattr reads the value of one xattr without following symlinks,
// growing the buffer until it fits.
func getXattr(path, name string) ([]byte, error) {
	for size := 256; ; size *= 2 {
		buf := make([]byte, size)
		n, err := lgetxattr(path, name, buf)
		if errors.Is(err, syscall.ERANGE) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return buf[:n], nil
	}
}

// The frozen stdlib syscall package only wraps the symlink-following
// xattr syscalls. The l* helpers below issue the raw non-following
// syscalls directly so every probe stays anchored to the entry the
// walk Lstat'd: a path swapped for a symlink mid-walk must not
// redirect a marker read or strip to the link's target.

// llistxattr lists the xattr names of path itself.
func llistxattr(path string, dest []byte) (int, error) {
	rawPath, err := syscall.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}
	var buf unsafe.Pointer
	if len(dest) > 0 {
		buf = unsafe.Pointer(&dest[0])
	}
	n, _, errno := syscall.Syscall(syscall.SYS_LLISTXATTR,
		uintptr(unsafe.Pointer(rawPath)), uintptr(buf), uintptr(len(dest)))
	if errno != 0 {
		return int(n), errno
	}
	return int(n), nil
}

// lgetxattr reads one xattr value of path itself.
func lgetxattr(path, name string, dest []byte) (int, error) {
	rawPath, err := syscall.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}
	rawName, err := syscall.BytePtrFromString(name)
	if err != nil {
		return 0, err
	}
	var buf unsafe.Pointer
	if len(dest) > 0 {
		buf = unsafe.Pointer(&dest[0])
	}
	n, _, errno := syscall.Syscall6(syscall.SYS_LGETXATTR,
		uintptr(unsafe.Pointer(rawPath)), uintptr(unsafe.Pointer(rawName)),
		uintptr(buf), uintptr(len(dest)), 0, 0)
	if errno != 0 {
		return int(n), errno
	}
	return int(n), nil
}

// lremovexattr removes one xattr from path itself.
func lremovexattr(path, name string) error {
	rawPath, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	rawName, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_LREMOVEXATTR,
		uintptr(unsafe.Pointer(rawPath)), uintptr(unsafe.Pointer(rawName)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// isNoXattrErr reports the errors that mean "no xattrs here": the
// path carries none, or its filesystem does not support them.
func isNoXattrErr(err error) bool {
	return errors.Is(err, syscall.ENODATA) || errors.Is(err, syscall.EOPNOTSUPP)
}

// splitXattrNames parses the NUL-separated listxattr buffer.
func splitXattrNames(buf []byte) []string {
	var names []string
	for _, part := range bytes.Split(buf, []byte{0}) {
		if len(part) > 0 {
			names = append(names, string(part))
		}
	}
	return names
}
