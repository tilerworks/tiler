package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const fileName = ".tiler.yaml"

// Load merges defaults, the user file, project files walked upward,
// and cli. home and fsRoot are injected so callers do not read the
// process home or walk the host root. A missing file is skipped. An
// unreadable file, invalid YAML, or unknown key fails the load and
// the error includes that file path. Invalid values are judged on
// the merged result. That error names the field and the last layer
// that set it: a user or project file path, or the command line.
//
// A relative path is resolved against the directory of the file that
// declared it. A relative command-line path is resolved against the
// process working directory. A ~/ prefix expands with home. Cleaning
// is lexical only and does not evaluate symlinks.
//
// ceiling, when non-empty and an ancestor of start, stops the upward
// project walk at that directory: the ceiling directory's own file is
// still applied, but nothing above it is. This bounds discovery inside
// a sandbox (for example a test workspace) so a stray .tiler.yaml higher
// up the tree cannot leak in. A ceiling that is not an ancestor of start
// is ignored, and the home user file is always applied regardless of it.
func Load(start, home, fsRoot, ceiling string, cli Patch) (Config, error) {
	if strings.TrimSpace(home) == "" {
		return Config{}, errors.New("home directory is empty")
	}
	home = filepath.Clean(home)
	var origin valueOrigin
	cfg, err := applyFile(Defaults(home), userFile(home), home, &origin)
	if err != nil {
		return Config{}, err
	}
	cfg, err = applyProjects(cfg, start, home, fsRoot, ceiling, &origin)
	if err != nil {
		return Config{}, err
	}
	cfg, err = applyCLI(cfg, cli, home, &origin)
	if err != nil {
		return Config{}, err
	}
	if err := Validate(cfg); err != nil {
		return Config{}, origin.wrap(err)
	}
	return cfg, nil
}

func userFile(home string) string {
	return filepath.Join(home, fileName)
}

func applyProjects(cfg Config, start, home, fsRoot, ceiling string, origin *valueOrigin) (Config, error) {
	paths, err := projectPaths(start, home, fsRoot, ceiling)
	if err != nil {
		return Config{}, err
	}
	user := userFile(home)
	for i := len(paths) - 1; i >= 0; i-- {
		if paths[i] == user {
			continue
		}
		cfg, err = applyFile(cfg, paths[i], home, origin)
		if err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

func applyFile(cfg Config, path, home string, origin *valueOrigin) (Config, error) {
	patch, ok, err := readPatch(path)
	if err != nil || !ok {
		return cfg, err
	}
	origin.note(patch, path)
	resolved := resolvePatch(patch, filepath.Dir(path), home)
	cfg = resolved.Apply(cfg)
	cfg.configFile = path
	return cfg, nil
}

func readPatch(path string) (Patch, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Patch{}, false, nil
		}
		return Patch{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	patch, err := Decode(data)
	if err != nil {
		return Patch{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return patch, true, nil
}

func applyCLI(cfg Config, cli Patch, home string, origin *valueOrigin) (Config, error) {
	base, err := commandBase(cli)
	if err != nil {
		return Config{}, err
	}
	origin.note(cli, commandLineSource)
	return resolvePatch(cli, base, home).Apply(cfg), nil
}

func commandBase(cli Patch) (string, error) {
	if !needsWorkingDir(cli) {
		return "", nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	return cwd, nil
}

func needsWorkingDir(cli Patch) bool {
	for _, raw := range []*string{cli.ImagesDir, cli.SessionsDir, cli.CacheDir} {
		if raw == nil || isHomePath(*raw) || filepath.IsAbs(*raw) {
			continue
		}
		return true
	}
	return false
}

func projectPaths(start, home, fsRoot, ceiling string) ([]string, error) {
	if strings.TrimSpace(start) == "" {
		return nil, errors.New("start directory is empty")
	}
	if strings.TrimSpace(fsRoot) == "" {
		return nil, errors.New("filesystem root is empty")
	}
	start = filepath.Clean(start)
	home = filepath.Clean(home)
	fsRoot = filepath.Clean(fsRoot)
	insideHome := isInside(start, home)
	if !insideHome && !isInside(start, fsRoot) {
		return nil, fmt.Errorf("start directory %s is outside filesystem root %s", start, fsRoot)
	}
	// A ceiling only binds the walk when it is an ancestor of start;
	// otherwise it is ignored and the home/fsRoot bounds stand.
	ceilingActive := false
	if strings.TrimSpace(ceiling) != "" {
		ceiling = filepath.Clean(ceiling)
		ceilingActive = isInside(start, ceiling)
	}
	var nearToFar []string
	for dir := start; ; dir = filepath.Dir(dir) {
		if insideHome && dir == home {
			break
		}
		nearToFar = append(nearToFar, filepath.Join(dir, fileName))
		if ceilingActive && dir == ceiling {
			break
		}
		if stopWalk(dir, home, fsRoot, insideHome) {
			break
		}
	}
	return nearToFar, nil
}

func stopWalk(dir, home, fsRoot string, insideHome bool) bool {
	parent := filepath.Dir(dir)
	if parent == dir {
		return true
	}
	if insideHome {
		return parent == home
	}
	return dir == fsRoot
}

func resolvePatch(p Patch, baseDir, home string) Patch {
	p.ImagesDir = resolvePathPtr(p.ImagesDir, baseDir, home)
	p.SessionsDir = resolvePathPtr(p.SessionsDir, baseDir, home)
	p.CacheDir = resolvePathPtr(p.CacheDir, baseDir, home)
	return p
}

func resolvePathPtr(raw *string, baseDir, home string) *string {
	if raw == nil {
		return nil
	}
	resolved := resolvePath(*raw, baseDir, home)
	return &resolved
}

func resolvePath(raw, baseDir, home string) string {
	if expanded, ok := expandHome(raw, home); ok {
		return filepath.Clean(expanded)
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	return filepath.Clean(filepath.Join(baseDir, raw))
}

func isHomePath(raw string) bool {
	return raw == "~" || strings.HasPrefix(raw, "~/")
}

func expandHome(raw, home string) (string, bool) {
	if !isHomePath(raw) {
		return "", false
	}
	suffix := strings.TrimPrefix(raw, "~")
	suffix = strings.TrimPrefix(suffix, "/")
	return filepath.Join(home, suffix), true
}

const commandLineSource = "command line"

// valueOrigin records the last layer that set each validated field.
// A lower invalid value replaced by a higher layer is not an error.
type valueOrigin struct {
	version       string
	logLevel      string
	keepVersions  string
	maxAge        string
	sessionMaxAge string
	splitSize     string
}

func (o *valueOrigin) note(p Patch, where string) {
	if p.Version != nil {
		o.version = where
	}
	if p.LogLevel != nil {
		o.logLevel = where
	}
	if p.KeepVersions != nil {
		o.keepVersions = where
	}
	if p.MaxAge != nil {
		o.maxAge = where
	}
	if p.SessionMaxAge != nil {
		o.sessionMaxAge = where
	}
	if p.SplitSize != nil {
		o.splitSize = where
	}
}

func (o valueOrigin) wrap(err error) error {
	where := ""
	switch {
	case strings.HasPrefix(err.Error(), "version:"):
		where = o.version
	case strings.HasPrefix(err.Error(), "log_level:"):
		where = o.logLevel
	case strings.HasPrefix(err.Error(), "keep_versions:"):
		where = o.keepVersions
	case strings.HasPrefix(err.Error(), "max_age:"):
		where = o.maxAge
	case strings.HasPrefix(err.Error(), "session_max_age:"):
		where = o.sessionMaxAge
	case strings.HasPrefix(err.Error(), "split_size:"):
		where = o.splitSize
	}
	if where == "" {
		return err
	}
	return fmt.Errorf("%s: %w", where, err)
}

func isInside(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
