package benchmark

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
)

func requireVolume(files FS, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", usagef("benchmark: path is required")
	}
	abs, err := absPath(path)
	if err != nil {
		return "", err
	}
	info, err := files.stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return "", usagef("benchmark: path %s does not exist", abs)
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", usagef("benchmark: path %s must be a directory", abs)
	}
	return abs, nil
}

func ensureWritable(files FS, path string, readonly bool) error {
	if readonly {
		return nil
	}
	if err := files.access(path); err != nil {
		if errors.Is(err, errNilSeam) {
			return err
		}
		return usagef("benchmark: %s is not writable; pass --readonly --image", path)
	}
	return nil
}

func placeBench(files FS, path, bench string, act actions) (string, error) {
	bench = strings.TrimSpace(bench)
	if bench == "" {
		return "", nil
	}
	if !act.usesBench() {
		return "", usagef("benchmark: --bench-dir is only used when creating or probing")
	}
	abs, err := absPath(bench)
	if err != nil {
		return "", err
	}
	if !strictChild(path, abs) {
		return "", usagef("benchmark: --bench-dir must be inside %s and must not equal it", path)
	}
	return emptyDir(files, abs, "--bench-dir")
}

func placeRuntime(files FS, path, runtime string) (string, bool, error) {
	if strings.TrimSpace(runtime) == "" {
		return "", true, nil
	}
	abs, err := absPath(runtime)
	if err != nil {
		return "", false, err
	}
	if sameOrNested(path, abs) {
		return "", false, usagef(
			"benchmark: --runtime-dir and the path must not contain each other")
	}
	info, err := files.stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return abs, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, usagef("benchmark: --runtime-dir %s must be a directory", abs)
	}
	return abs, false, nil
}

func placeSrc(files FS, src string, create bool) (string, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", nil
	}
	if !create {
		return "", usagef("benchmark: --src is only valid when creating an image")
	}
	abs, err := absPath(src)
	if err != nil {
		return "", err
	}
	info, err := files.stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return "", usagef("benchmark: --src %s does not exist", abs)
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", usagef("benchmark: --src %s must be a directory", abs)
	}
	return abs, nil
}

func placeImage(image string) (string, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return "", nil
	}
	return absPath(image)
}

func emptyDir(files FS, abs, flag string) (string, error) {
	info, err := files.stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return abs, nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", usagef("benchmark: %s %s must be a directory", flag, abs)
	}
	entries, err := files.readDir(abs)
	if err != nil {
		return "", err
	}
	if len(entries) > 0 {
		return "", usagef("benchmark: %s %s exists and is not empty", flag, abs)
	}
	return abs, nil
}

func sameOrNested(a, b string) bool {
	return a == b || strictChild(a, b) || strictChild(b, a)
}

func strictChild(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	sep := ".." + string(filepath.Separator)
	return rel != ".." && !strings.HasPrefix(rel, sep)
}
