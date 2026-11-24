//go:build darwin

package checkpoint

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/durable"
	"golang.org/x/sys/unix"
)

func requireIdentity(f *os.File, want restoreIdentity) error {
	got, err := identity(f)
	if err == nil && (want.Inode == 0 || got != want) {
		err = errors.New("directory identity changed")
	}
	return err
}

func removeTemporaryCopy(p *Parent, name string, want restoreIdentity) error {
	return removeDarwinTree(p, name, want, true)
}

func removeArchive(p *Parent, name string, want restoreIdentity) error {
	return removeDarwinTree(p, name, want, false)
}

func removeDarwinTree(p *Parent, name string, want restoreIdentity, writable bool) error {
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
	var st unix.Stat_t
	err := unix.Fstatat(int(p.File.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || (restoreIdentity{Device: uint64(st.Dev), Inode: st.Ino}) != want {
		return errors.New("directory identity changed")
	}
	if err := removeDarwinEntry(p.File, name, path, st, writable); err != nil {
		return err
	}
	return durable.Sync(p.File)
}

func removeDarwinEntry(parent *os.File, name, path string, before unix.Stat_t, writable bool) error {
	flags := 0
	if before.Mode&unix.S_IFMT == unix.S_IFDIR {
		if err := checkTreeMounts(path); err != nil {
			return err
		}
		dir, err := openEntry(parent, name, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			return err
		}
		defer closeMaybe(dir)
		if err := verifyDarwinFile(dir, before); err != nil {
			return err
		}
		if writable {
			if err := dir.Chmod(0700); err != nil {
				return err
			}
		}
		names, err := dir.Readdirnames(-1)
		if err != nil {
			return err
		}
		for _, child := range names {
			var st unix.Stat_t
			if err := unix.Fstatat(int(dir.Fd()), child, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
			if st.Dev != before.Dev {
				return errors.New("cross-filesystem entry")
			}
			if err := removeDarwinEntry(dir, child, filepath.Join(path, child), st, writable); err != nil {
				return err
			}
		}
		if err := durable.Sync(dir); err != nil {
			return err
		}
		if err := checkTreeMounts(path); err != nil {
			return err
		}
		flags = unix.AT_REMOVEDIR
	}
	if err := verifyDarwinName(parent, name, before); err != nil {
		return err
	}
	return unix.Unlinkat(int(parent.Fd()), name, flags)
}

func verifyDarwinName(parent *os.File, name string, before unix.Stat_t) error {
	var after unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	return compareDarwinIdentity(before, after)
}

func verifyDarwinFile(f *os.File, before unix.Stat_t) error {
	var after unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &after); err != nil {
		return err
	}
	return compareDarwinIdentity(before, after)
}

func compareDarwinIdentity(before, after unix.Stat_t) error {
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode&unix.S_IFMT != after.Mode&unix.S_IFMT {
		return errors.New("directory entry changed")
	}
	return nil
}
