//go:build linux || darwin

package checkpoint

import (
	"os"
	"syscall"
)

func identity(f *os.File) (restoreIdentity, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return restoreIdentity{}, err
	}
	return restoreIdentity{Device: uint64(st.Dev), Inode: st.Ino}, nil
}

func closeMaybe(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}
