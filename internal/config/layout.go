package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrReservedPath means a path sits inside a reserved tree or would
// cover one. Callers map it to a usage error when the path was a
// mount target or a build source.
var ErrReservedPath = errors.New("reserved path")

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
		{name: "logs_dir", dir: cfg.LogsDir},
	}
}

// OutsideReserved returns an error when path equals a reserved root
// or lies inside one, and nil when path is outside every reserved
// tree. The reserved trees are the cleaned $HOME/.tiler root and its
// descendants, plus the resolved images, sessions, cache, and logs
// directories. Per-mount runtime lives under sessions_dir and is
// covered by that root. Comparison uses real paths when the path
// and each reserved root exist, so a symlink into a reserved tree
// is refused the same way as a lexical descendant. A missing path
// or reserved root falls back to its absolute lexical form.
func OutsideReserved(path string, cfg Config) error {
	abs, err := resolveComparePath(path)
	if err != nil {
		return fmt.Errorf("reserved tree: %w", err)
	}
	for _, root := range reservedRoots(cfg) {
		if root.dir == "" {
			continue
		}
		clean, err := resolveComparePath(root.dir)
		if err != nil {
			return fmt.Errorf("reserved tree: %w", err)
		}
		if !isInside(abs, clean) {
			continue
		}
		return fmt.Errorf("%w: %q is inside reserved %s %q",
			ErrReservedPath, abs, root.name, clean)
	}
	return nil
}

// CoversReserved returns an error when path is a strict ancestor of a
// reserved root, so a mount or build at path would hide that tree
// under the view or pack the library and runtime into a new image.
// Equality with a reserved root is left to OutsideReserved.
// Comparison uses real paths when they exist, so a symlink that
// resolves to an ancestor is refused. A missing path or reserved
// root falls back to its absolute lexical form.
func CoversReserved(path string, cfg Config) error {
	abs, err := resolveComparePath(path)
	if err != nil {
		return fmt.Errorf("reserved tree: %w", err)
	}
	for _, root := range reservedRoots(cfg) {
		if root.dir == "" {
			continue
		}
		clean, err := resolveComparePath(root.dir)
		if err != nil {
			return fmt.Errorf("reserved tree: %w", err)
		}
		if abs == clean || !isInside(clean, abs) {
			continue
		}
		return fmt.Errorf("%w: %q would cover reserved %s %q",
			ErrReservedPath, abs, root.name, clean)
	}
	return nil
}

// resolveComparePath is the absolute path used for reserved-tree
// checks. When every component exists, symlinks are evaluated so a
// link into or over a reserved tree cannot hide. When a trailing
// component is missing, the deepest existing ancestor is resolved
// and the missing suffix is rejoined, so /var and /private/var stay
// comparable on macOS.
func resolveComparePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if !reservedPathMissing(err) {
		return "", err
	}
	return resolveMissingPath(abs)
}

// resolveMissingPath resolves the deepest existing ancestor of abs
// and appends the missing trailing components.
func resolveMissingPath(abs string) (string, error) {
	var missing []string
	cur := abs
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			parts := append([]string{resolved}, reverseBase(missing)...)
			return filepath.Clean(filepath.Join(parts...)), nil
		}
		if !reservedPathMissing(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
}

func reverseBase(names []string) []string {
	out := make([]string, len(names))
	for i, name := range names {
		out[len(names)-1-i] = name
	}
	return out
}

func reservedPathMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
