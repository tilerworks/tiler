//go:build linux

package checkpoint

import (
	"os"
	"syscall"
	"unsafe"
)

func unlinkAt(parent *os.File, name string, flags uintptr) error {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, e := syscall.Syscall(syscall.SYS_UNLINKAT, parent.Fd(), uintptr(unsafe.Pointer(p)), flags)
	if e != 0 {
		return e
	}
	return nil
}
