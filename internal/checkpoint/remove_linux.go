//go:build linux

package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jiangxin/tiler/internal/durable"
)

// removeArchive removes only the recorded directory and never follows links.
// The caller holds the sibling lock; external writers are not excluded.
func removeArchive(p *Parent, name string, want restoreIdentity) error {
	return removeArchiveWith(p, name, want, unlinkAt)
}

func removeArchiveWith(p *Parent, name string, want restoreIdentity, unlink func(*os.File, string, uintptr) error) error {
	return removeArchiveTree(p, name, want, unlink, false)
}

// removeTemporaryCopy permits changing directory modes only in the recorded
// unpublished copy, so read-only copied directories can be removed on retry.
func removeTemporaryCopy(p *Parent, name string, want restoreIdentity) error {
	return removeArchiveTree(p, name, want, unlinkAt, true)
}

func removeArchiveTree(p *Parent, name string, want restoreIdentity, unlink func(*os.File, string, uintptr) error, writable bool) error {
	if err := safeName(name); err != nil {
		return err
	}
	if err := p.Verify(); err != nil {
		return err
	}
	path := filepath.Join(p.path, name)
	if err := checkTreeMounts(path); err != nil {
		return err
	}
	var dir *os.File
	var err error
	if writable {
		pathFD, openErr := openEntry(p.File, name, 0x200000|syscall.O_DIRECTORY)
		if errors.Is(openErr, syscall.ENOENT) {
			return nil
		}
		if openErr != nil {
			return openErr
		}
		if err = requireIdentity(pathFD, want); err != nil {
			_ = pathFD.Close()
			return err
		}
		var st syscall.Stat_t
		if err = syscall.Fstat(int(pathFD.Fd()), &st); err == nil {
			dir, err = openCopyDir(p.File, name, pathFD, &st)
		}
		_ = pathFD.Close()
	} else {
		dir, err = openEntry(p.File, name, syscall.O_RDONLY|syscall.O_DIRECTORY)
	}
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if err := requireIdentity(dir, want); err != nil {
		return err
	}
	if err := removeArchiveContents(dir, path, unlink, writable); err != nil {
		return err
	}
	if err := checkTreeMounts(path); err != nil {
		return err
	}
	if err := p.Verify(); err != nil {
		return err
	}
	current, err := openEntry(p.File, name, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	if err := requireIdentity(current, want); err != nil {
		return err
	}
	if err := unlink(p.File, name, 0x200); err != nil {
		return err
	}
	return durable.Sync(p.File)
}

func requireIdentity(f *os.File, want restoreIdentity) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if want.Inode == 0 || uint64(st.Dev) != want.Device || st.Ino != want.Inode {
		return errors.New("archive identity changed")
	}
	return nil
}

func removeArchiveContents(dir *os.File, path string, unlink func(*os.File, string, uintptr) error, writable bool) error {
	if writable {
		if err := syscall.Fchmod(int(dir.Fd()), 0700); err != nil {
			return err
		}
	}
	if err := checkTreeMounts(path); err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if err := safeName(name); err != nil {
			return err
		}
		if err := removeArchiveEntry(dir, path, name, unlink, writable); err != nil {
			return fmt.Errorf("remove %s: %w", filepath.Join(path, name), err)
		}
	}
	return durable.Sync(dir)
}

func removeArchiveEntry(parent *os.File, path, name string, unlink func(*os.File, string, uintptr) error, writable bool) error {
	const oPath = 0x200000
	fd, err := syscall.Openat(int(parent.Fd()), name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	entry := os.NewFile(uintptr(fd), name)
	defer func() { _ = entry.Close() }()
	var before syscall.Stat_t
	if err := syscall.Fstat(fd, &before); err != nil {
		return err
	}
	if before.Mode&syscall.S_IFMT == syscall.S_IFDIR {
		var child *os.File
		if writable {
			child, err = openCopyDir(parent, name, entry, &before)
		} else {
			child, err = openEntry(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY)
		}
		if err != nil {
			return err
		}
		defer func() { _ = child.Close() }()
		if err = verifyEntry(child, &before); err != nil {
			return err
		}
		if err = removeArchiveContents(child, filepath.Join(path, name), unlink, writable); err != nil {
			return err
		}
		if err = verifyNamed(parent, name, &before); err != nil {
			return err
		}
		return unlink(parent, name, 0x200)
	}
	if err = verifyNamed(parent, name, &before); err != nil {
		return err
	}
	return unlink(parent, name, 0)
}

// openCopyDir opens a recorded copy directory. Mode 000 prevents a normal
// open; on Linux procfs can chmod the already opened O_PATH inode without
// following a replaceable directory entry.
func openCopyDir(parent *os.File, name string, pathFD *os.File, before *syscall.Stat_t) (*os.File, error) {
	child, err := openEntry(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if errors.Is(err, syscall.EACCES) {
		var fs syscall.Statfs_t
		if err = syscall.Statfs("/proc/self/fd", &fs); err != nil {
			return nil, err
		}
		const procSuperMagic = 0x9fa0
		if uint64(fs.Type) != procSuperMagic {
			return nil, errors.New("cannot safely change inaccessible copy directory mode without procfs")
		}
		if err = os.Chmod(fmt.Sprintf("/proc/self/fd/%d", pathFD.Fd()), 0700); err != nil {
			return nil, err
		}
		child, err = openEntry(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY)
	}
	if err != nil {
		return nil, err
	}
	if err = verifyEntry(child, before); err != nil {
		_ = child.Close()
		return nil, err
	}
	return child, nil
}

func verifyNamed(parent *os.File, name string, before *syscall.Stat_t) error {
	const oPath = 0x200000
	fd, err := syscall.Openat(int(parent.Fd()), name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	return verifyEntry(f, before)
}
