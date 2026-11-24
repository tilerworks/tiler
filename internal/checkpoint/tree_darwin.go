//go:build darwin

package checkpoint

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/durable"
	"golang.org/x/sys/unix"
)

type darwinTreeCopy struct {
	device      int32
	root        *os.File
	clonePolicy clonePolicy
	links       map[uint64]darwinCopyLink
}

type darwinCopyLink struct {
	path string
	stat unix.Stat_t
}

// CopyTree copies a directory using relative, no-follow operations.
func CopyTree(src, dst *os.File) error {
	path, err := directoryPath(src)
	if err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(src.Fd()), &st); err != nil {
		return err
	}
	state := darwinTreeCopy{device: st.Dev, root: dst, links: make(map[uint64]darwinCopyLink)}
	if err := state.copyDir(src, dst, path, "", st); err != nil {
		return err
	}
	return setDarwinTimes(dst, ".", dst, st)
}

func (s *darwinTreeCopy) copyDir(src, dst *os.File, path, rel string, st unix.Stat_t) error {
	if err := checkTreeMounts(path); err != nil {
		return err
	}
	names, err := src.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := safeName(name); err != nil {
			return err
		}
		if rel == "" && name == MarkerName {
			continue
		}
		if err := s.copyEntry(src, dst, path, rel, name); err != nil {
			return fmt.Errorf("copy %s: %w", filepath.Join(rel, name), err)
		}
	}
	if err := checkTreeMounts(path); err != nil {
		return err
	}
	return copyDarwinMetadata(src, dst, st)
}

func (s *darwinTreeCopy) copyEntry(src, dst *os.File, path, rel, name string) error {
	var st unix.Stat_t
	if err := unix.Fstatat(int(src.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Dev != s.device {
		return errors.New("cross-filesystem entry")
	}
	flags := unix.O_RDONLY | unix.O_NONBLOCK
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		flags |= unix.O_DIRECTORY
	case unix.S_IFLNK:
		flags |= unix.O_SYMLINK
	case unix.S_IFREG:
	default:
		return errors.New("unsupported special file")
	}
	sf, err := openEntry(src, name, flags)
	if err != nil {
		return err
	}
	defer closeMaybe(sf)
	if err := verifyDarwinFile(sf, st); err != nil {
		return err
	}

	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		if err := unix.Mkdirat(int(dst.Fd()), name, 0700); err != nil {
			return err
		}
		df, err := openEntry(dst, name, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			return err
		}
		defer closeMaybe(df)
		if err := s.copyDir(sf, df, filepath.Join(path, name), filepath.Join(rel, name), st); err != nil {
			return err
		}
		return setDarwinTimes(dst, name, df, st)
	case unix.S_IFLNK:
		return copyDarwinSymlink(src, dst, sf, name, st)
	default:
		return s.copyRegular(sf, dst, rel, name, st)
	}
}

var darwinClone = unix.Fclonefileat

func (s *darwinTreeCopy) copyRegular(src, dst *os.File, rel, name string, st unix.Stat_t) error {
	if old, ok := s.links[st.Ino]; ok {
		if err := verifyDarwinName(s.root, old.path, old.stat); err != nil {
			return err
		}
		if err := unix.Linkat(int(s.root.Fd()), old.path, int(dst.Fd()), name, 0); err != nil {
			return err
		}
		return verifyDarwinName(dst, name, old.stat)
	}
	var err error
	fallback := s.clonePolicy.unavailable
	if !fallback {
		err = darwinClone(int(src.Fd()), int(dst.Fd()), name, unix.CLONE_NOOWNERCOPY)
		var policyErr error
		fallback, policyErr = s.clonePolicy.fallback(err, func() (bool, error) {
			return probeClone(dst, src, func(_ *os.File, probeSrc *os.File, probeName string) error {
				return darwinClone(int(probeSrc.Fd()), int(dst.Fd()), probeName, unix.CLONE_NOOWNERCOPY)
			})
		})
		if policyErr != nil {
			return policyErr
		}
	}
	if err != nil && !fallback {
		return fmt.Errorf("clone file: %w", err)
	}
	flags := unix.O_RDONLY | unix.O_NONBLOCK
	if fallback {
		flags = unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_NONBLOCK
	}
	fd, err := unix.Openat(int(dst.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	df := os.NewFile(uintptr(fd), name)
	defer closeMaybe(df)
	if fallback {
		if _, err := io.Copy(df, src); err != nil {
			return err
		}
	}
	if err := copyDarwinMetadata(src, df, st); err != nil {
		return err
	}
	if err := setDarwinTimes(dst, name, df, st); err != nil {
		return err
	}
	if st.Nlink > 1 {
		var created unix.Stat_t
		if err := unix.Fstat(fd, &created); err != nil {
			return err
		}
		s.links[st.Ino] = darwinCopyLink{path: filepath.Join(rel, name), stat: created}
	}
	return nil
}

func copyDarwinSymlink(src, dst, sf *os.File, name string, st unix.Stat_t) error {
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(int(src.Fd()), name, buf)
	if err != nil {
		return err
	}
	if n == len(buf) {
		return errors.New("symlink target exceeds buffer")
	}
	if err := verifyDarwinName(src, name, st); err != nil {
		return err
	}
	if err := unix.Symlinkat(string(buf[:n]), int(dst.Fd()), name); err != nil {
		return err
	}
	df, err := openEntry(dst, name, unix.O_SYMLINK|unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer closeMaybe(df)
	if err := copyDarwinMetadata(sf, df, st); err != nil {
		return err
	}
	return setDarwinTimes(dst, name, df, st)
}

func setDarwinTimes(parent *os.File, name string, file *os.File, source unix.Stat_t) error {
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return err
	}
	if err := verifyDarwinName(parent, name, st); err != nil {
		return err
	}
	if err := unix.UtimesNanoAt(int(parent.Fd()), name, []unix.Timespec{source.Atim, source.Mtim}, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if err := verifyDarwinName(parent, name, st); err != nil {
		return err
	}
	if source.Mode&unix.S_IFMT == unix.S_IFLNK {
		return durable.Sync(parent)
	}
	return durable.Sync(file)
}
