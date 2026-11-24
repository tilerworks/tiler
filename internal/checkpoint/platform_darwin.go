//go:build darwin

package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/jiangxin/tiler/internal/filelock"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"golang.org/x/sys/unix"
)

func openDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openEntry(parent *os.File, name string, flags int) (*os.File, error) {
	// O_SYMLINK opens the link itself. Combining it with O_NOFOLLOW
	// causes Darwin to reject the link with ELOOP.
	if flags&unix.O_SYMLINK == 0 {
		flags |= unix.O_NOFOLLOW
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// Exchange atomically swaps two entries in the pinned parent.
func (p *Parent) Exchange(first, second string) error {
	return renameDarwin(p.File, first, p.File, second, unix.RENAME_SWAP)
}

// RenameNoReplace atomically renames an entry without replacing another.
func (p *Parent) RenameNoReplace(old, dest string) error {
	return renameDarwin(p.File, old, p.File, dest, unix.RENAME_EXCL)
}

var renameDarwinAt = unix.RenameatxNp

func renameDarwin(src *os.File, old string, dst *os.File, name string, flags uint32) error {
	if err := safeName(old); err != nil {
		return err
	}
	if err := safeName(name); err != nil {
		return err
	}
	return renameDarwinAt(int(src.Fd()), old, int(dst.Fd()), name, flags)
}

// Lock acquires the persistent sibling lock relative to the pinned parent.
func (p *Parent) Lock(name string) (*os.File, error) {
	if err := safeName(name); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(p.File.Fd()), name, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	var st syscall.Stat_t
	err = syscall.Fstat(fd, &st)
	if err == nil && st.Mode&unix.S_IFMT != unix.S_IFREG {
		err = errors.New("lock is not a regular file")
	}
	if err == nil {
		err = filelock.Acquire(f, 0)
	}
	if err == nil {
		err = verifyNamed(p.File, name, &st)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func checkpointMounts() ([]mountinfo.Mount, error) {
	points, err := darwinMountPoints()
	if err != nil {
		return nil, err
	}
	mounts := make([]mountinfo.Mount, 0, len(points))
	for _, point := range points {
		mounts = append(mounts, mountinfo.Mount{Point: point})
	}
	return mounts, nil
}

func checkTreeMounts(root string) error {
	mounts, err := checkpointMounts()
	if err != nil {
		return err
	}
	for _, mount := range mounts {
		if within(mount.Point, root) {
			return errors.New("mount point in tree: " + mount.Point)
		}
	}
	return nil
}

func unlinkAt(parent *os.File, name string, flags uintptr) error {
	return unix.Unlinkat(int(parent.Fd()), name, int(flags))
}

func verifyNamed(parent *os.File, name string, before *syscall.Stat_t) error {
	var after unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode&unix.S_IFMT != after.Mode&unix.S_IFMT {
		return errors.New("directory entry changed")
	}
	return nil
}

func publishExchangeStage(p *Parent, stage string, _ *os.File, dest *os.File, name string) error {
	// The source name was checked against the recorded inode by the caller.
	// EXCL preserves any intervening destination, and recovery checks its inode.
	return renameDarwin(p.File, stage, dest, name, unix.RENAME_EXCL)
}

func directoryPath(dir *os.File) (string, error) {
	var buf [unix.PathMax]byte
	//nolint:staticcheck // x/sys does not expose a buffer-based fcntl wrapper.
	_, _, err := syscall.Syscall(unix.SYS_FCNTL, dir.Fd(), unix.F_GETPATH, uintptr(unsafe.Pointer(&buf[0])))
	if err != 0 {
		return "", err
	}
	return unix.ByteSliceToString(buf[:]), nil
}

func checkpointPathSpelling(path string, exists bool) (string, error) {
	dir := path
	if !exists {
		dir = filepath.Dir(path)
	}
	f, err := openDirectory(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	physical, err := directoryPath(f)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(physical) {
		return "", errors.New("invalid directory path from fcntl")
	}
	if !exists {
		physical = filepath.Join(physical, filepath.Base(path))
	}
	return physical, nil
}
