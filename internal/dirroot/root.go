//go:build linux || darwin

// Package dirroot provides descriptor-relative operations on Linux and
// Darwin with Go 1.22. Relative symlinks and parent components are resolved
// within the opened tree; absolute paths and absolute symlink targets are
// rejected. This is a limited internal replacement for os.Root: create
// operations reject final symlinks, and RemoveAll refuses the opened root.
package dirroot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// maxSymlinks bounds symlink expansion. Ordinary components are not
// counted: a deep tree without links is limited by the kernel, and each
// ".." shortens the component list, so it cannot cycle on its own.
const maxSymlinks = 8

// errPathEscapes is returned when a path or symlink would leave the root.
// Callers match the substring "escapes".
var errPathEscapes = errors.New("path escapes from parent")

// Root is a directory and the operations performed inside it.
type Root struct {
	name   string
	mu     sync.Mutex
	fd     int
	closed bool
}

// Open opens the named directory. Later operations stay inside it.
func Open(name string) (*Root, error) {
	fd, err := syscall.Open(name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return &Root{name: name, fd: fd}, nil
}

// Close releases the directory. A second call does nothing.
func (r *Root) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	err := syscall.Close(r.fd)
	r.fd = -1
	return err
}

// Open opens name for reading.
func (r *Root) Open(name string) (*os.File, error) {
	return r.OpenFile(name, os.O_RDONLY, 0)
}

// OpenFile opens name using the given flags. The permission bits must
// fit in 0o777. The final component of a create is not followed, so
// O_CREATE cannot replace a symlink with its target.
func (r *Root) OpenFile(name string, flag int, perm os.FileMode) (*os.File, error) {
	if perm&^0o777 != 0 {
		return nil, opErr("openat", name, errors.New("unsupported file mode"))
	}
	parent, base, err := r.locate(name, flag&os.O_CREATE == 0)
	if err != nil {
		return nil, opErr("openat", name, err)
	}
	defer func() { _ = syscall.Close(parent) }()
	fd, err := openat(parent, base, flag|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(perm))
	if err != nil {
		return nil, opErr("openat", name, err)
	}
	return os.NewFile(uintptr(fd), joinRoot(r.name, name)), nil
}

// Stat returns the description of name, following symlinks that stay
// inside the root.
func (r *Root) Stat(name string) (fs.FileInfo, error) {
	parent, base, err := r.locate(name, true)
	if err != nil {
		return nil, opErr("stat", name, err)
	}
	defer func() { _ = syscall.Close(parent) }()
	info, err := lstatat(parent, base)
	if err != nil {
		return nil, opErr("stat", name, err)
	}
	return info, nil
}

// Lstat returns the description of name. A final symlink is described
// as a symlink.
func (r *Root) Lstat(name string) (fs.FileInfo, error) {
	parent, base, err := r.locate(name, false)
	if err != nil {
		return nil, opErr("lstat", name, err)
	}
	defer func() { _ = syscall.Close(parent) }()
	info, err := lstatat(parent, base)
	if err != nil {
		return nil, opErr("lstat", name, err)
	}
	return info, nil
}

// Remove removes the named file, symlink, or empty directory.
func (r *Root) Remove(name string) error {
	parent, base, err := r.locate(name, false)
	if err != nil {
		return opErr("remove", name, err)
	}
	defer func() { _ = syscall.Close(parent) }()
	info, err := lstatat(parent, base)
	if err != nil {
		return opErr("remove", name, err)
	}
	flags := 0
	if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		flags = atRemovedir
	}
	return opErr("remove", name, unlinkat(parent, base, flags))
}

// RemoveAll removes name and anything under it. A symlink is removed
// without visiting its target. A missing name is success. A final "."
// is rejected before anything is deleted, matching os.RemoveAll.
func (r *Root) RemoveAll(name string) error {
	parts, endsSlash, err := splitPath(name)
	if err != nil {
		return opErr("removeall", name, err)
	}
	if parts[len(parts)-1] == "." && !endsSlash {
		return opErr("removeall", name, syscall.EINVAL)
	}
	parent, base, err := r.locate(name, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return opErr("removeall", name, err)
	}
	defer func() { _ = syscall.Close(parent) }()
	if base == "." {
		// Walking up to the root, or naming ".", must not delete the
		// opened directory. A trailing slash after a dot (dir/./) is
		// the directory itself and is handled below.
		rootfd, dupErr := r.dup()
		if dupErr != nil {
			return opErr("removeall", name, dupErr)
		}
		defer func() { _ = syscall.Close(rootfd) }()
		same, serr := sameDir(parent, rootfd)
		if serr != nil {
			return opErr("removeall", name, serr)
		}
		if same {
			return opErr("removeall", name, syscall.EINVAL)
		}
	}
	err = removeTree(parent, base)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return opErr("removeall", name, err)
}

// Rename renames oldname to newname. Both paths are inside the root.
func (r *Root) Rename(oldname, newname string) error {
	oldParent, oldBase, err := r.locate(oldname, false)
	if err != nil {
		return opErr("rename", oldname, err)
	}
	defer func() { _ = syscall.Close(oldParent) }()
	newParent, newBase, err := r.locate(newname, false)
	if err != nil {
		return opErr("rename", newname, err)
	}
	defer func() { _ = syscall.Close(newParent) }()
	return opErr("rename", oldname, renameat(oldParent, oldBase, newParent, newBase))
}

// WriteFile writes data to name, creating or truncating it.
func (r *Root) WriteFile(name string, data []byte, perm os.FileMode) error {
	file, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	return err
}

// FS adapts the root to io/fs. Paths use forward slashes.
func (r *Root) FS() fs.FS { return rootFS{r} }

type rootFS struct{ r *Root }

func (f rootFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &os.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return f.r.Open(name)
}

func (f rootFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &os.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	parent, base, err := f.r.locate(name, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = syscall.Close(parent) }()
	// Even "." needs a new open file description: dup shares the
	// directory cursor with the persistent root descriptor.
	dirfd, err := openat(parent, base, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, opErr("readdir", name, err)
	}
	defer func() { _ = syscall.Close(dirfd) }()
	names, err := namesFromFD(dirfd)
	if err != nil {
		return nil, opErr("readdir", name, err)
	}
	entries := make([]fs.DirEntry, 0, len(names))
	for _, child := range names {
		info, lerr := lstatat(dirfd, child)
		if errors.Is(lerr, os.ErrNotExist) {
			continue
		}
		if lerr != nil {
			return nil, opErr("readdir", name, lerr)
		}
		entries = append(entries, dirEntry{info.(fileInfo)})
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return entries, nil
}

type dirEntry struct{ fileInfo }

func (d dirEntry) Type() fs.FileMode          { return d.mode.Type() }
func (d dirEntry) Info() (fs.FileInfo, error) { return d.fileInfo, nil }

func (r *Root) dup() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return -1, os.ErrClosed
	}
	return dupCloexec(r.fd)
}

// locate returns a duplicate file descriptor for the parent of the last
// path component. followFinal reports whether a symlink in that component
// is resolved. The caller closes the returned descriptor.
func (r *Root) locate(name string, followFinal bool) (int, string, error) {
	parts, endsSlash, err := splitPath(name)
	if err != nil {
		return -1, "", err
	}
	symlinks := 0
	for {
		parent, err := r.dup()
		if err != nil {
			return -1, "", err
		}
		restarted := false
		for i := 0; i < len(parts); i++ {
			part := parts[i]
			last := i == len(parts)-1
			if part == ".." {
				// The preceding component was opened as a directory in this
				// pass. Pop that resolved name and walk again from the root.
				_ = syscall.Close(parent)
				if i == 0 {
					return -1, "", errPathEscapes
				}
				parts = append(append([]string{}, parts[:i-1]...), parts[i+1:]...)
				if len(parts) == 0 {
					parts = []string{"."}
				}
				restarted = true
				break
			}
			info, lerr := lstatat(parent, part)
			if lerr != nil {
				// A trailing slash is a directory requirement, not a
				// name to create.
				if last && !followFinal && !endsSlash {
					return parent, part, nil
				}
				_ = syscall.Close(parent)
				return -1, "", lerr
			}
			followLink := info.Mode()&fs.ModeSymlink != 0 && (!last || followFinal || endsSlash)
			if followLink {
				next, ferr := retarget(&symlinks, &endsSlash, last, parent, part, parts[:i], parts[i+1:])
				if ferr != nil {
					return -1, "", ferr
				}
				parts = next
				restarted = true
				break
			}
			if last {
				if endsSlash && !info.IsDir() {
					_ = syscall.Close(parent)
					return -1, "", syscall.ENOTDIR
				}
				return parent, part, nil
			}
			if !info.IsDir() {
				_ = syscall.Close(parent)
				return -1, "", syscall.ENOTDIR
			}
			fd, oerr := openat(parent, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
			if oerr != nil {
				if errors.Is(oerr, syscall.ELOOP) {
					next, ferr := retarget(&symlinks, &endsSlash, last, parent, part, parts[:i], parts[i+1:])
					if ferr != nil {
						return -1, "", ferr
					}
					parts = next
					restarted = true
					break
				}
				_ = syscall.Close(parent)
				return -1, "", oerr
			}
			_ = syscall.Close(parent)
			parent = fd
		}
		if !restarted {
			_ = syscall.Close(parent)
			return -1, "", syscall.ENOENT
		}
	}
}

func retarget(symlinks *int, endsSlash *bool, last bool, parent int, part string, prefix, suffix []string) ([]string, error) {
	next, linkSlash, err := takeLink(symlinks, parent, part, prefix, suffix)
	if err != nil {
		return nil, err
	}
	// A slash or dot on the link target belongs to that target. It
	// becomes the path's own directory requirement only when nothing
	// follows the link. A suffix is resolved inside the target, so the
	// requirement stays on the target's last component.
	if last && linkSlash {
		*endsSlash = true
	}
	return next, nil
}

func takeLink(symlinks *int, parent int, part string, prefix, suffix []string) ([]string, bool, error) {
	target, err := readlinkat(parent, part)
	_ = syscall.Close(parent)
	if err != nil {
		return nil, false, err
	}
	*symlinks++
	if *symlinks > maxSymlinks {
		return nil, false, syscall.ELOOP
	}
	return spliceLink(target, prefix, suffix)
}

func splitPath(name string) ([]string, bool, error) {
	if strings.IndexByte(name, 0) >= 0 {
		return nil, false, errPathEscapes
	}
	return splitPathInRoot(name, nil, nil)
}

// splitPathInRoot splits s and joins it with prefix and suffix.
// "." is kept only as the final component, because that is what makes
// the preceding object a directory. endsInSlash is true when s itself
// ends in a slash. ".." is left for the walker, which opens the
// preceding component before applying it.
func splitPathInRoot(s string, prefix, suffix []string) ([]string, bool, error) {
	if s == "" {
		return nil, false, errors.New("empty path")
	}
	if s[0] == '/' {
		return nil, false, errPathEscapes
	}
	parts := make([]string, 0, len(prefix)+len(suffix)+4)
	parts = append(parts, prefix...)
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && s[j] != '/' {
			j++
		}
		parts = append(parts, s[i:j])
		slash := j
		for j < len(s) && s[j] == '/' {
			j++
		}
		if j == len(s) {
			ends := slash < len(s)
			if len(suffix) > 0 && parts[len(parts)-1] == "." {
				parts = parts[:len(parts)-1]
			}
			parts = append(parts, suffix...)
			if len(parts) == 0 {
				parts = []string{"."}
			}
			return parts, ends, nil
		}
		if parts[len(parts)-1] == "." {
			parts = parts[:len(parts)-1]
		}
		i = j
	}
	return nil, false, errors.New("empty path")
}

func spliceLink(target string, prefix, suffix []string) ([]string, bool, error) {
	if target == "" || strings.IndexByte(target, 0) >= 0 || target[0] == '/' {
		return nil, false, errPathEscapes
	}
	return splitPathInRoot(target, prefix, suffix)
}

func removeTree(dirfd int, name string) error {
	info, err := lstatat(dirfd, name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return unlinkat(dirfd, name, 0)
	}
	fd, err := openat(dirfd, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	names, readErr := namesFromFD(fd)
	if readErr != nil {
		_ = syscall.Close(fd)
		return readErr
	}
	if err := removeTreeChildren(fd, names); err != nil {
		_ = syscall.Close(fd)
		return err
	}
	_ = syscall.Close(fd)
	return unlinkat(dirfd, name, atRemovedir)
}

func removeTreeChildren(fd int, names []string) error {
	for _, child := range names {
		// A child can disappear after the directory was listed. Its
		// absence completes only that child's deletion, not its siblings'.
		if err := removeTree(fd, child); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func namesFromFD(fd int) ([]string, error) {
	dupfd, err := dupCloexec(fd)
	if err != nil {
		return nil, err
	}
	// The display name is unused. Readdirnames does not stat, so a
	// basename cannot redirect the lookup at the working directory.
	file := os.NewFile(uintptr(dupfd), ".")
	names, err := file.Readdirnames(-1)
	cerr := file.Close()
	if err != nil {
		return nil, err
	}
	return names, cerr
}

func sameDir(a, b int) (bool, error) {
	var sa, sb syscall.Stat_t
	if err := syscall.Fstat(a, &sa); err != nil {
		return false, err
	}
	if err := syscall.Fstat(b, &sb); err != nil {
		return false, err
	}
	return sa.Dev == sb.Dev && sa.Ino == sb.Ino, nil
}

func joinRoot(root, name string) string {
	if name == "." {
		return root
	}
	return filepath.Join(root, name)
}

func opErr(op, name string, err error) error {
	if err == nil {
		return nil
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return err
	}
	return &os.PathError{Op: op, Path: name, Err: err}
}

type fileInfo struct {
	name  string
	mode  fs.FileMode
	size  int64
	mtime time.Time
	stat  *syscall.Stat_t
}

func (fi fileInfo) Name() string       { return fi.name }
func (fi fileInfo) Size() int64        { return fi.size }
func (fi fileInfo) Mode() fs.FileMode  { return fi.mode }
func (fi fileInfo) ModTime() time.Time { return fi.mtime }
func (fi fileInfo) IsDir() bool        { return fi.mode.IsDir() }
func (fi fileInfo) Sys() any           { return fi.stat }

func modeFrom(m uint32) fs.FileMode {
	mode := fs.FileMode(m & 0o777)
	if m&syscall.S_ISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if m&syscall.S_ISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if m&syscall.S_ISVTX != 0 {
		mode |= fs.ModeSticky
	}
	switch m & syscall.S_IFMT {
	case syscall.S_IFBLK:
		mode |= fs.ModeDevice
	case syscall.S_IFCHR:
		mode |= fs.ModeDevice | fs.ModeCharDevice
	case syscall.S_IFDIR:
		mode |= fs.ModeDir
	case syscall.S_IFIFO:
		mode |= fs.ModeNamedPipe
	case syscall.S_IFLNK:
		mode |= fs.ModeSymlink
	case syscall.S_IFSOCK:
		mode |= fs.ModeSocket
	}
	return mode
}
