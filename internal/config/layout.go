package config

import (
	"fmt"
	"path/filepath"
)

// OutsideReserved returns an error when path equals a reserved root
// or lies inside one, and nil when path is outside every reserved
// tree. The reserved trees are the cleaned $HOME/.tiler root and its
// descendants, plus the resolved images, sessions, and cache
// directories. Per-mount runtime lives under sessions_dir and is
// covered by that root. Comparison is lexical. The path does not need
// to exist, and symlinks are not evaluated.
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
