//go:build linux

package dirroot

import (
	"io/fs"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

// oPath opens a file without using its content. Combined with O_NOFOLLOW
// it describes a symlink itself. The value is the same on every Linux
// architecture; the syscall package does not export it for all of them.
const (
	oPath       = 0x200000
	atRemovedir = 0x200
)

func dupCloexec(fd int) (int, error) {
	nfd, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_DUPFD_CLOEXEC), 0)
	if errno != 0 {
		return -1, errno
	}
	return int(nfd), nil
}

func fdFlags(fd int) (int, error) {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_GETFD), 0)
	if errno != 0 {
		return 0, errno
	}
	return int(flags), nil
}

func openat(dirfd int, name string, flags int, perm uint32) (int, error) {
	return syscall.Openat(dirfd, name, flags|syscall.O_CLOEXEC, perm)
}

func lstatat(dirfd int, name string) (fs.FileInfo, error) {
	fd, err := openat(dirfd, name, oPath|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = syscall.Close(fd) }()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return nil, err
	}
	return fileInfo{name: name, mode: modeFrom(uint32(st.Mode)), size: int64(st.Size), mtime: time.Unix(st.Mtim.Sec, st.Mtim.Nsec), stat: &st}, nil
}

func readlinkat(dirfd int, name string) (string, error) {
	for size := 128; size <= 64*1024; size *= 2 {
		buf := make([]byte, size)
		p, err := syscall.BytePtrFromString(name)
		if err != nil {
			return "", err
		}
		n, _, e := syscall.Syscall6(syscall.SYS_READLINKAT, uintptr(dirfd), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
		runtime.KeepAlive(p)
		if e != 0 {
			return "", e
		}
		if int(n) < len(buf) {
			return string(buf[:n]), nil
		}
	}
	return "", syscall.ENAMETOOLONG
}

func unlinkat(dirfd int, name string, flags int) error {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, e := syscall.Syscall(syscall.SYS_UNLINKAT, uintptr(dirfd), uintptr(unsafe.Pointer(p)), uintptr(flags))
	runtime.KeepAlive(p)
	if e != 0 {
		return e
	}
	return nil
}

func renameat(olddirfd int, oldname string, newdirfd int, newname string) error {
	return syscall.Renameat(olddirfd, oldname, newdirfd, newname)
}
