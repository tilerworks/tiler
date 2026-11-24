// Package checkpoint provides the safety primitives for directory checkpoints.
package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/mountinfo"
)

// Resolve resolves a directory or a missing final target, including dangling
// final symlinks. A missing parent is rejected.
func Resolve(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty path")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for i := 0; i < 40; i++ {
		parent, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return "", fmt.Errorf("resolve parent: %w", err)
		}
		path = filepath.Join(parent, filepath.Base(path))
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return checkpointPathSpelling(path, false)
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if info.IsDir() {
				return checkpointPathSpelling(path, true)
			}
			return path, nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(parent, target)
		}
		path = filepath.Clean(target)
	}
	return "", errors.New("too many symlinks")
}

func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CheckPath rejects protected and mounted trees. Mount data must be obtained
// immediately before use, and rechecked at destructive boundaries.
func CheckPath(path, home string, cfg config.Config, mounts []mountinfo.Mount) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path must be absolute and clean")
	}
	if within(home, path) || filepath.Dir(path) == string(filepath.Separator) || path == string(filepath.Separator) {
		return fmt.Errorf("protected path: %s", path)
	}
	if err := config.OutsideReserved(path, cfg); err != nil {
		return err
	}
	if err := config.CoversReserved(path, cfg); err != nil {
		return err
	}
	if err := checkPlatformProtectedPaths(path, home, cfg); err != nil {
		return err
	}
	for _, m := range mounts {
		if within(filepath.Clean(m.Point), path) {
			return fmt.Errorf("mount point in path: %s", m.Point)
		}
	}
	return nil
}

// Parent pins the directory used for relative filesystem operations.
type Parent struct {
	File *os.File
	path string
	info os.FileInfo
}

// OpenParent opens and verifies the resolved parent directory.
func OpenParent(path string) (*Parent, error) {
	parent := filepath.Dir(path)
	f, err := openDirectory(parent)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	p := &Parent{File: f, path: parent, info: info}
	if err := p.Verify(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return p, nil
}

// Verify detects replacement of the path naming the pinned parent.
func (p *Parent) Verify() error {
	info, err := os.Lstat(p.path)
	if err != nil {
		return err
	}
	if !info.IsDir() || !os.SameFile(p.info, info) {
		return errors.New("parent directory changed")
	}
	return nil
}

// Close releases the pinned directory.
func (p *Parent) Close() error { return p.File.Close() }
