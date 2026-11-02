//go:build darwin

package dirroot

import (
	"io/fs"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

const (
	atSymlinkNoFollow = 0x20
	atRemovedir       = 0x80
)

//go:linkname openat syscall.openat
func openat(fd int, path string, flags int, perm uint32) (int, error)

//go:linkname unlinkat syscall.unlinkat
func unlinkat(fd int, path string, flags int) error

//go:linkname fstatat syscall.fstatat
func fstatat(fd int, path string, stat *syscall.Stat_t, flags int) error

//go:linkname syscall6 syscall.syscall6
func syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

//go:linkname fcntl syscall.fcntl
func fcntl(fd int, cmd int, arg int) (int, error)

func dupCloexec(fd int) (int, error) {
	return fcntl(fd, syscall.F_DUPFD_CLOEXEC, 0)
}

func fdFlags(fd int) (int, error) {
	return fcntl(fd, syscall.F_GETFD, 0)
}

func lstatat(dirfd int, name string) (fs.FileInfo, error) {
	var st syscall.Stat_t
	if err := fstatat(dirfd, name, &st, atSymlinkNoFollow); err != nil {
		return nil, err
	}
	return fileInfo{name: name, mode: modeFrom(uint32(st.Mode)), size: int64(st.Size), mtime: time.Unix(st.Mtimespec.Sec, st.Mtimespec.Nsec), stat: &st}, nil
}

func readlinkat(dirfd int, name string) (string, error) {
	for size := 128; size <= 64*1024; size *= 2 {
		buf := make([]byte, size)
		p, err := syscall.BytePtrFromString(name)
		if err != nil {
			return "", err
		}
		n, _, e := syscall6(libcReadlinkatAddr, uintptr(dirfd), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
		runtime.KeepAlive(p)
		runtime.KeepAlive(buf)
		if e != 0 {
			return "", e
		}
		if int(n) < len(buf) {
			return string(buf[:n]), nil
		}
	}
	return "", syscall.ENAMETOOLONG
}

func renameat(olddirfd int, oldname string, newdirfd int, newname string) error {
	oldp, err := syscall.BytePtrFromString(oldname)
	if err != nil {
		return err
	}
	newp, err := syscall.BytePtrFromString(newname)
	if err != nil {
		return err
	}
	_, _, e := syscall6(libcRenameatAddr, uintptr(olddirfd), uintptr(unsafe.Pointer(oldp)), uintptr(newdirfd), uintptr(unsafe.Pointer(newp)), 0, 0)
	runtime.KeepAlive(oldp)
	runtime.KeepAlive(newp)
	if e != 0 {
		return e
	}
	return nil
}

var libcReadlinkatAddr uintptr

//go:cgo_import_dynamic libc_readlinkat readlinkat "/usr/lib/libSystem.B.dylib"

var libcRenameatAddr uintptr

//go:cgo_import_dynamic libc_renameat renameat "/usr/lib/libSystem.B.dylib"
