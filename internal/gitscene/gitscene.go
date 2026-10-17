// Package gitscene sanitizes a git source directory before it is
// packed into an image. Git is reached only through exec, never
// through a library binding, and nothing here clones or fetches.
package gitscene

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner executes one external command with dir as the working
// directory and returns its standard output. Tests inject a
// recorder; the default runs git through exec.
type Runner func(dir, name string, args ...string) ([]byte, error)

// leakyFragments are lowercase name fragments that mark a local
// config key as able to carry credentials or redirect remotes.
var leakyFragments = []string{"credential", "insteadof", "token"}

// Prepare sanitizes the git repository in dir for imaging.
//
// It removes the origin remote, reflogs, FETCH_HEAD, and non-sample
// hooks, unsets local config keys that can leak credentials, and
// pre-generates the commit-graph and multi-pack-index. Any git
// failure aborts the whole preparation, so a caller never packs a
// directory with half-written indexes or leftover credentials.
func Prepare(dir string) error {
	return prepare(dir, runGit)
}

// IsGit reports whether dir looks like a git work tree or a bare
// repository. It only checks the filesystem, so a non-git directory
// never triggers a git command.
func IsGit(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	head, headErr := os.Lstat(filepath.Join(dir, "HEAD"))
	objects, objErr := os.Lstat(filepath.Join(dir, "objects"))
	return headErr == nil && objErr == nil && !head.IsDir() && objects.IsDir()
}

func prepare(dir string, run Runner) error {
	bare, err := detectBare(dir, run)
	if err != nil {
		return err
	}
	gitDir, err := resolveGitDir(dir, bare)
	if err != nil {
		return err
	}
	if err := removeOrigin(dir, run); err != nil {
		return err
	}
	if err := cleanFiles(gitDir); err != nil {
		return err
	}
	if err := unsetLeakyConfig(dir, run); err != nil {
		return err
	}
	return writeIndexes(dir, gitDir, run)
}

// detectBare verifies dir is a git repository and reports whether it
// is bare.
func detectBare(dir string, run Runner) (bool, error) {
	out, err := run(dir, "git", "rev-parse", "--is-inside-work-tree")
	if err == nil && strings.TrimSpace(string(out)) == "true" {
		return false, nil
	}
	out, bareErr := run(dir, "git", "rev-parse", "--is-bare-repository")
	if bareErr == nil && strings.TrimSpace(string(out)) == "true" {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("gitscene: %w", err)
	}
	return false, errors.New("gitscene: not a git repository")
}

// resolveGitDir finds the administrative directory without invoking
// git: .git as a directory, .git as a gitdir pointer file, or the
// repository itself when bare.
func resolveGitDir(dir string, bare bool) (string, error) {
	if bare {
		return dir, nil
	}
	dot := filepath.Join(dir, ".git")
	info, err := os.Stat(dot)
	if err != nil {
		return "", fmt.Errorf("gitscene: %w", err)
	}
	if info.IsDir() {
		return dot, nil
	}
	data, err := os.ReadFile(dot)
	if err != nil {
		return "", fmt.Errorf("gitscene: %w", err)
	}
	target, ok := gitDirTarget(string(data))
	if !ok {
		return "", errors.New("gitscene: malformed .git file")
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return filepath.Clean(target), nil
}

func gitDirTarget(content string) (string, bool) {
	line, _, _ := strings.Cut(content, "\n")
	return strings.CutPrefix(strings.TrimSpace(line), "gitdir: ")
}

// removeOrigin drops the origin remote when it exists. Clone and
// fetch stay out of scope, so no remote is worth keeping.
func removeOrigin(dir string, run Runner) error {
	out, err := run(dir, "git", "remote")
	if err != nil {
		return fmt.Errorf("gitscene: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "origin" {
			continue
		}
		if _, err := run(dir, "git", "remote", "remove", "origin"); err != nil {
			return fmt.Errorf("gitscene: %w", err)
		}
		return nil
	}
	return nil
}

// cleanFiles drops reflogs, fetch state, and non-sample hooks.
func cleanFiles(gitDir string) error {
	if err := os.RemoveAll(filepath.Join(gitDir, "logs")); err != nil {
		return fmt.Errorf("gitscene: %w", err)
	}
	fetch := filepath.Join(gitDir, "FETCH_HEAD")
	if err := os.Remove(fetch); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gitscene: %w", err)
	}
	return cleanHooks(filepath.Join(gitDir, "hooks"))
}

func cleanHooks(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gitscene: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".sample") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return fmt.Errorf("gitscene: %w", err)
		}
	}
	return nil
}

// unsetLeakyConfig removes every local config key whose name can
// carry credentials or redirect a remote URL.
func unsetLeakyConfig(dir string, run Runner) error {
	out, err := run(dir, "git", "config", "--local", "--list", "--name-only")
	if err != nil {
		return fmt.Errorf("gitscene: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		key := strings.TrimSpace(line)
		if key == "" || !isLeakyKey(key) {
			continue
		}
		if _, err := run(dir, "git", "config", "--local", "--unset-all", key); err != nil {
			return fmt.Errorf("gitscene: %w", err)
		}
	}
	return nil
}

func isLeakyKey(key string) bool {
	lower := strings.ToLower(key)
	for _, frag := range leakyFragments {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return false
}

// writeIndexes pre-generates the read-side indexes an image consumer
// would otherwise build on first use. The multi-pack-index only
// applies to pack files, so a repository of loose objects alone
// skips it instead of failing the preparation.
func writeIndexes(dir, gitDir string, run Runner) error {
	if _, err := run(dir, "git", "commit-graph", "write", "--reachable"); err != nil {
		return fmt.Errorf("gitscene: %w", err)
	}
	packs, err := filepath.Glob(filepath.Join(gitDir, "objects", "pack", "*.pack"))
	if err != nil {
		return fmt.Errorf("gitscene: %w", err)
	}
	if len(packs) == 0 {
		return nil
	}
	if _, err := run(dir, "git", "multi-pack-index", "write"); err != nil {
		return fmt.Errorf("gitscene: %w", err)
	}
	return nil
}

// runGit is the default Runner. A non-zero exit carries the trimmed
// stderr output.
func runGit(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return nil, fmt.Errorf("%s: %w: %s", name, err, bytes.TrimSpace(exit.Stderr))
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}
