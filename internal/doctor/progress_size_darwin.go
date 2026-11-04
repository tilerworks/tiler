//go:build darwin

package doctor

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

// readProgressSize queries the actual terminal; character devices such as
// /dev/null fail the ioctl and therefore never receive cursor controls.
func readProgressSize(w io.Writer) (int, int) {
	file, ok := w.(*os.File)
	if !ok {
		return 0, 0
	}
	var size struct {
		rows, columns, xpixel, ypixel uint16
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(),
		syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)))
	if errno != 0 {
		return 0, 0
	}
	return int(size.columns), int(size.rows)
}
