//go:build !linux && !darwin

package dirroot

import (
	"errors"
	"io/fs"
	"os"
)

// errUnsupported is returned on hosts that have no openat directory root.
var errUnsupported = errors.New("dirroot requires linux or darwin")

// Root is a directory and the operations performed inside it.
type Root struct{}

// Open reports that this host cannot limit lookups to a directory fd.
func Open(name string) (*Root, error) {
	return nil, &os.PathError{Op: "open", Path: name, Err: errUnsupported}
}

// Close reports that the root was not opened.
func (r *Root) Close() error { return errUnsupported }

// Open reports that the root was not opened.
func (r *Root) Open(name string) (*os.File, error) {
	return nil, &os.PathError{Op: "open", Path: name, Err: errUnsupported}
}

// OpenFile reports that the root was not opened.
func (r *Root) OpenFile(name string, _ int, _ os.FileMode) (*os.File, error) {
	return nil, &os.PathError{Op: "open", Path: name, Err: errUnsupported}
}

// Stat reports that the root was not opened.
func (r *Root) Stat(name string) (fs.FileInfo, error) {
	return nil, &os.PathError{Op: "stat", Path: name, Err: errUnsupported}
}

// Lstat reports that the root was not opened.
func (r *Root) Lstat(name string) (fs.FileInfo, error) {
	return nil, &os.PathError{Op: "lstat", Path: name, Err: errUnsupported}
}

// Remove reports that the root was not opened.
func (r *Root) Remove(name string) error {
	return &os.PathError{Op: "remove", Path: name, Err: errUnsupported}
}

// RemoveAll reports that the root was not opened.
func (r *Root) RemoveAll(name string) error {
	return &os.PathError{Op: "removeall", Path: name, Err: errUnsupported}
}

// Rename reports that the root was not opened.
func (r *Root) Rename(oldname, _ string) error {
	return &os.PathError{Op: "rename", Path: oldname, Err: errUnsupported}
}

// WriteFile reports that the root was not opened.
func (r *Root) WriteFile(name string, _ []byte, _ os.FileMode) error {
	return &os.PathError{Op: "write", Path: name, Err: errUnsupported}
}

// FS reports that the root was not opened.
func (r *Root) FS() fs.FS { return unsupportedFS{} }

type unsupportedFS struct{}

func (unsupportedFS) Open(name string) (fs.File, error) {
	return nil, &os.PathError{Op: "open", Path: name, Err: errUnsupported}
}
