//go:build linux

package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"syscall"
	"unsafe"
)

var cloneTreeFile = cloneFile

type inodeKey struct {
	dev uint64
	ino uint64
}
type copyTreeState struct {
	rootDev     uint64
	links       map[inodeKey]string
	dstRoot     *os.File
	clonePolicy clonePolicy
}

// CopyTree copies an opened source directory into an empty opened destination.
// It refuses mounted and special entries and never follows source symlinks.
func CopyTree(src, dst *os.File) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(src.Fd()), &st); err != nil {
		return err
	}
	root, err := os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(src.Fd()), 10))
	if err != nil {
		return err
	}
	if err = checkTreeMounts(root); err != nil {
		return err
	}
	state := &copyTreeState{rootDev: uint64(st.Dev), links: make(map[inodeKey]string), dstRoot: dst}
	if err = state.copyDir(src, dst, ""); err != nil {
		return err
	}
	return checkTreeMounts(root)
}

func (s *copyTreeState) copyDir(src, dst *os.File, rel string) error {
	entries, err := src.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if err := safeName(name); err != nil {
			return err
		}
		if rel == "" && name == MarkerName {
			continue
		}
		if err := s.copyEntry(src, dst, name, filepath.Join(rel, name)); err != nil {
			return fmt.Errorf("copy %s: %w", filepath.Join(rel, name), err)
		}
	}
	if err := copyMetadata(src, dst); err != nil {
		return err
	}
	if err := dst.Sync(); err != nil {
		return err
	}
	return nil
}

func (s *copyTreeState) copyEntry(src, dst *os.File, name, rel string) error {
	const oPath = 0x200000
	fd, err := syscall.Openat(int(src.Fd()), name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	probe := os.NewFile(uintptr(fd), name)
	defer func() { _ = probe.Close() }()
	var st syscall.Stat_t
	if err = syscall.Fstat(fd, &st); err != nil {
		return err
	}
	if uint64(st.Dev) != s.rootDev {
		return errors.New("cross-filesystem entry")
	}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		if err = syscall.Mkdirat(int(dst.Fd()), name, 0700); err != nil {
			return err
		}
		sf, err := openEntry(src, name, syscall.O_RDONLY|syscall.O_DIRECTORY)
		if err != nil {
			return err
		}
		defer func() { _ = sf.Close() }()
		if err = verifyEntry(sf, &st); err != nil {
			return err
		}
		df, err := openEntry(dst, name, syscall.O_RDONLY|syscall.O_DIRECTORY)
		if err != nil {
			return err
		}
		defer func() { _ = df.Close() }()
		return s.copyDir(sf, df, rel)
	case syscall.S_IFREG:
		return s.copyRegular(src, dst, name, rel, &st)
	case syscall.S_IFLNK:
		return copySymlink(src, dst, probe, name, &st)
	default:
		return errors.New("unsupported special file")
	}
}

func openEntry(parent *os.File, name string, flags int) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func verifyEntry(f *os.File, want *syscall.Stat_t) error {
	var got syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &got); err != nil {
		return err
	}
	if got.Dev != want.Dev || got.Ino != want.Ino || got.Mode&syscall.S_IFMT != want.Mode&syscall.S_IFMT {
		return errors.New("source entry changed")
	}
	return nil
}
func (s *copyTreeState) copyRegular(src, dst *os.File, name, rel string, st *syscall.Stat_t) error {
	sf, err := openEntry(src, name, syscall.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { _ = sf.Close() }()
	if err = verifyEntry(sf, st); err != nil {
		return err
	}
	key := inodeKey{uint64(st.Dev), st.Ino}
	if old, ok := s.links[key]; ok {
		return linkAt(s.dstRoot, old, dst, name)
	}
	fd, err := syscall.Openat(int(dst.Fd()), name, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	df := os.NewFile(uintptr(fd), name)
	defer func() { _ = df.Close() }()
	clone := func(to, from *os.File) error {
		if s.clonePolicy.unavailable {
			return errCloneUnsupported
		}
		cloneErr := cloneTreeFile(to, from)
		fallback, policyErr := s.clonePolicy.fallback(cloneErr, func() (bool, error) {
			return probeClone(dst, from, func(probeDst, probeSrc *os.File, _ string) error { return cloneTreeFile(probeDst, probeSrc) })
		})
		if policyErr != nil {
			return policyErr
		}
		if fallback {
			return errCloneUnsupported
		}
		return cloneErr
	}
	if err = copyFileData(df, sf, clone); err != nil {
		return err
	}
	if err = copyMetadata(sf, df); err != nil {
		return err
	}
	if err = df.Sync(); err != nil {
		return err
	}
	s.links[key] = rel
	return nil
}
func linkAt(src *os.File, old string, dst *os.File, name string) error {
	a, err := syscall.BytePtrFromString(old)
	if err != nil {
		return err
	}
	b, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, e := syscall.Syscall6(syscall.SYS_LINKAT, src.Fd(), uintptr(unsafe.Pointer(a)), dst.Fd(), uintptr(unsafe.Pointer(b)), 0, 0)
	if e != 0 {
		return e
	}
	return nil
}
func copyMetadata(src, dst *os.File) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(src.Fd()), &st); err != nil {
		return err
	}
	if err := syscall.Fchown(int(dst.Fd()), int(st.Uid), int(st.Gid)); err != nil {
		return err
	}
	if err := syscall.Fchmod(int(dst.Fd()), st.Mode&07777); err != nil {
		return err
	}
	if err := copyXattrs(src, dst); err != nil {
		return err
	}
	ts := [2]syscall.Timespec{st.Atim, st.Mtim}
	_, _, e := syscall.Syscall6(syscall.SYS_UTIMENSAT, dst.Fd(), 0, uintptr(unsafe.Pointer(&ts[0])), 0, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}
func xattrCall(op uintptr, f *os.File, name string, data []byte) (int, error) {
	var n *byte
	var err error
	if name != "" {
		n, err = syscall.BytePtrFromString(name)
		if err != nil {
			return 0, err
		}
	}
	var p unsafe.Pointer
	if len(data) > 0 {
		p = unsafe.Pointer(&data[0])
	}
	var a, b, c uintptr
	if op == syscall.SYS_FLISTXATTR {
		a = uintptr(p)
		b = uintptr(len(data))
	} else {
		a = uintptr(unsafe.Pointer(n))
		b = uintptr(p)
		c = uintptr(len(data))
	}
	r, _, e := syscall.Syscall6(op, f.Fd(), a, b, c, 0, 0)
	if e != 0 {
		return 0, e
	}
	return int(r), nil
}
func copyXattrs(src, dst *os.File) error {
	n, err := xattrCall(syscall.SYS_FLISTXATTR, src, "", nil)
	if errors.Is(err, syscall.ENOTSUP) {
		return nil
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	buf := make([]byte, n)
	n, err = xattrCall(syscall.SYS_FLISTXATTR, src, "", buf)
	if err != nil {
		return err
	}
	for _, name := range strings.Split(string(buf[:n]), "\x00") {
		if name == "" {
			continue
		}
		size, err := xattrCall(syscall.SYS_FGETXATTR, src, name, nil)
		if err != nil {
			return err
		}
		value := make([]byte, size)
		n, err := xattrCall(syscall.SYS_FGETXATTR, src, name, value)
		if err != nil {
			return err
		}
		if _, err = xattrCall(syscall.SYS_FSETXATTR, dst, name, value[:n]); err != nil {
			return err
		}
	}
	return nil
}

func copySymlink(src, dst, pinned *os.File, name string, st *syscall.Stat_t) error {
	empty, _ := syscall.BytePtrFromString("")
	buf := make([]byte, 4096)
	n, _, e := syscall.Syscall6(syscall.SYS_READLINKAT, pinned.Fd(), uintptr(unsafe.Pointer(empty)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	if e != 0 {
		return e
	}
	if int(n) == len(buf) {
		return errors.New("symlink target too long")
	}
	target, err := syscall.BytePtrFromString(string(buf[:n]))
	if err != nil {
		return err
	}
	entry, _ := syscall.BytePtrFromString(name)
	_, _, e = syscall.Syscall(syscall.SYS_SYMLINKAT, uintptr(unsafe.Pointer(target)), dst.Fd(), uintptr(unsafe.Pointer(entry)))
	if e != 0 {
		return e
	}
	srcPath := fmt.Sprintf("/proc/self/fd/%d/%s", src.Fd(), name)
	dstPath := fmt.Sprintf("/proc/self/fd/%d/%s", dst.Fd(), name)
	if err := verifyLinkPath(srcPath, st); err != nil {
		return err
	}
	if err := syscall.Fchownat(int(dst.Fd()), name, int(st.Uid), int(st.Gid), 0x100); err != nil {
		return err
	}
	if err := copyLinkXattrs(srcPath, dstPath); err != nil {
		return err
	}
	ts := [2]syscall.Timespec{st.Atim, st.Mtim}
	_, _, e = syscall.Syscall6(syscall.SYS_UTIMENSAT, dst.Fd(), uintptr(unsafe.Pointer(entry)), uintptr(unsafe.Pointer(&ts[0])), 0x100, 0, 0)
	if e != 0 {
		return e
	}
	return verifyLinkPath(srcPath, st)
}

func verifyLinkPath(path string, want *syscall.Stat_t) error {
	var got syscall.Stat_t
	if err := syscall.Lstat(path, &got); err != nil {
		return err
	}
	if got.Dev != want.Dev || got.Ino != want.Ino || got.Mode&syscall.S_IFMT != syscall.S_IFLNK {
		return errors.New("source symlink changed")
	}
	return nil
}

func linkXattrCall(op uintptr, path, name string, data []byte) (int, error) {
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}
	var np *byte
	if name != "" {
		np, err = syscall.BytePtrFromString(name)
		if err != nil {
			return 0, err
		}
	}
	var dataPtr unsafe.Pointer
	if len(data) > 0 {
		dataPtr = unsafe.Pointer(&data[0])
	}
	var a, b, c uintptr
	if op == syscall.SYS_LLISTXATTR {
		a = uintptr(dataPtr)
		b = uintptr(len(data))
	} else {
		a = uintptr(unsafe.Pointer(np))
		b = uintptr(dataPtr)
		c = uintptr(len(data))
	}
	n, _, e := syscall.Syscall6(op, uintptr(unsafe.Pointer(p)), a, b, c, 0, 0)
	if e != 0 {
		return 0, e
	}
	return int(n), nil
}
func copyLinkXattrs(src, dst string) error {
	n, err := linkXattrCall(syscall.SYS_LLISTXATTR, src, "", nil)
	if errors.Is(err, syscall.ENOTSUP) {
		return nil
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	buf := make([]byte, n)
	n, err = linkXattrCall(syscall.SYS_LLISTXATTR, src, "", buf)
	if err != nil {
		return err
	}
	for _, name := range strings.Split(string(buf[:n]), "\x00") {
		if name == "" {
			continue
		}
		size, err := linkXattrCall(syscall.SYS_LGETXATTR, src, name, nil)
		if err != nil {
			return err
		}
		value := make([]byte, size)
		n, err = linkXattrCall(syscall.SYS_LGETXATTR, src, name, value)
		if err != nil {
			return err
		}
		if _, err = linkXattrCall(syscall.SYS_LSETXATTR, dst, name, value[:n]); err != nil {
			return err
		}
	}
	return nil
}
