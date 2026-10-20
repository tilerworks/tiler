package config

import (
	"fmt"
	"path/filepath"
)

// reservedRoot names one tree that must stay outside every mount
// target and every build source that walks the workspace.
type reservedRoot struct {
	name string
	dir  string
}

func reservedRoots(cfg Config) []reservedRoot {
	return []reservedRoot{
		{name: "home layout", dir: cfg.reservedRoot},
		{name: "images_dir", dir: cfg.ImagesDir},
		{name: "sessions_dir", dir: cfg.SessionsDir},
		{name: "cache_dir", dir: cfg.CacheDir},
	}
}

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
	for _, root := range reservedRoots(cfg) {
		if root.dir == "" || !isInside(abs, root.dir) {
			continue
		}
		return fmt.Errorf("%s is inside reserved %s %s", abs, root.name, filepath.Clean(root.dir))
	}
	return nil
}

// CoversReserved returns an error when path is a strict ancestor of a
// reserved root, so a mount at path would hide that tree under the
// view and place upper/work/lower storage inside the merged tree.
// Equality with a reserved root is left to OutsideReserved. Comparison
// is lexical. The path does not need to exist, and symlinks are not
// evaluated.
func CoversReserved(path string, cfg Config) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("reserved tree: %w", err)
	}
	abs = filepath.Clean(abs)
	for _, root := range reservedRoots(cfg) {
		if root.dir == "" {
			continue
		}
		clean := filepath.Clean(root.dir)
		if abs == clean || !isInside(clean, abs) {
			continue
		}
		return fmt.Errorf("%s would cover reserved %s %s", abs, root.name, clean)
	}
	return nil
}
