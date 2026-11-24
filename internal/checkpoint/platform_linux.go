//go:build linux

package checkpoint

import (
	"errors"
	"os"
	"syscall"
	"time"
	"unsafe"

	"github.com/jiangxin/tiler/internal/filelock"
	"golang.org/x/sys/unix"
)

func openDirectory(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// Exchange atomically swaps two sibling entries without a missing-path window.
func (p *Parent) Exchange(first, second string) error {
	return p.exchangeWith(first, second, unix.Renameat2)
}

func (p *Parent) exchangeWith(first, second string, exchange func(int, string, int, string, uint) error) error {
	if err := safeName(first); err != nil {
		return err
	}
	if err := safeName(second); err != nil {
		return err
	}
	return exchange(int(p.File.Fd()), first, int(p.File.Fd()), second, unix.RENAME_EXCHANGE)
}

// Lock acquires a persistent sibling lock without following a symlink.
func (p *Parent) Lock(name string) (*os.File, error) {
	return p.lockWith(name, func(f *os.File) error { return filelock.Acquire(f, time.Duration(0)) })
}

func (p *Parent) lockWith(name string, acquire func(*os.File) error) (*os.File, error) {
	if err := safeName(name); err != nil {
		return nil, err
	}
	fd, err := syscall.Openat(int(p.File.Fd()), name, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("lock is not a regular file")
	}
	if err == nil {
		err = acquire(f)
	}
	if err == nil {
		var opened, named unix.Stat_t
		err = unix.Fstat(fd, &opened)
		if err == nil {
			err = unix.Fstatat(int(p.File.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW)
		}
		if err == nil && (opened.Dev != named.Dev || opened.Ino != named.Ino || opened.Mode&unix.S_IFMT != named.Mode&unix.S_IFMT) {
			err = errors.New("lock entry changed")
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// RenameNoReplace atomically moves an entry without overwriting a destination.
func (p *Parent) RenameNoReplace(old, dest string) error {
	if err := safeName(old); err != nil {
		return err
	}
	if err := safeName(dest); err != nil {
		return err
	}
	a, err := syscall.BytePtrFromString(old)
	if err != nil {
		return err
	}
	b, err := syscall.BytePtrFromString(dest)
	if err != nil {
		return err
	}
	_, _, e := syscall.Syscall6(renameat2Number, p.File.Fd(), uintptr(unsafe.Pointer(a)), p.File.Fd(), uintptr(unsafe.Pointer(b)), 1, 0)
	if e != 0 {
		return e
	}
	return nil
}
