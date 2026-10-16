package config

import (
	"fmt"
	"path/filepath"
)

// OutsideReserved returns an error when path equals a reserved root
// or lies inside one, and nil when path is outside every reserved
// tree. The reserved trees are the cleaned $HOME/.tiler root and its
// descendants, plus the resolved images, runtime, sessions, and cache
// directories. Comparison is lexical. The path does not need to exist,
// and symlinks are not evaluated.
func OutsideReserved(path string, cfg Config) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("reserved tree: %w", err)
	}
	roots := []struct {
		name string
		dir  string
	}{
		{name: "home layout", dir: cfg.reservedRoot},
		{name: "images_dir", dir: cfg.ImagesDir},
		{name: "runtime_dir", dir: cfg.RuntimeDir},
		{name: "sessions_dir", dir: cfg.SessionsDir},
		{name: "cache_dir", dir: cfg.CacheDir},
	}
	for _, root := range roots {
		if root.dir == "" || !isInside(abs, root.dir) {
			continue
		}
		return fmt.Errorf("%s is inside reserved %s %s", abs, root.name, filepath.Clean(root.dir))
	}
	return nil
}

// RuntimeDirName returns the runtime directory name for a mount
// point's device and inode. The same pair always yields the same
// name, so two mounts of one path share a directory and two devices
// do not collide when their inode numbers match.
//
// Inode reuse after delete and recreate makes this a directory name
// only, not an identity. A later session record and the mount point
// path decide identity. The caller supplies device and inode; this
// function does not stat.
func RuntimeDirName(dev, ino uint64) string {
	return fmt.Sprintf("up-%d-%d", dev, ino)
}
