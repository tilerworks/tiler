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

// defaultBenchDir is the --bench-dir name used when the flag was not
// set explicitly. The --src default reuses payloadDir. Plan joins it
// under the benchmark path.
const defaultBenchDir = "tiler-bench"

// relativeSub validates a relative subdirectory passed via flag and
// returns its cleaned form. It rejects empty values, absolute paths,
// paths starting with ./ or ../ (including a bare . or ..), paths
// that clean to the current directory, and paths with any .. element,
// so a joined result always stays inside its parent.
func relativeSub(flag, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", usagef("benchmark: %s must not be empty", flag)
	}
	if filepath.IsAbs(raw) {
		return "", usagef("benchmark: %s must be a relative path", flag)
	}
	sep := string(filepath.Separator)
	if raw == "." || raw == ".." ||
		strings.HasPrefix(raw, "."+sep) || strings.HasPrefix(raw, ".."+sep) {
		return "", usagef("benchmark: %s must not start with ./ or ../", flag)
	}
	// Scan the raw elements so an embedded .. such as a/../b is
	// refused too, even though Clean would fold it away.
	for _, elem := range strings.Split(raw, sep) {
		if elem == ".." {
			return "", usagef("benchmark: %s must not contain ..", flag)
		}
	}
	cleaned := filepath.Clean(raw)
	if cleaned == "." {
		return "", usagef("benchmark: %s must name a subdirectory", flag)
	}
	return cleaned, nil
}

// placeBench resolves the relative --bench-dir under the benchmark
// path and requires the joined directory to be absent or empty. The
// default applies when the flag was not set explicitly. When the run
// uses no bench dir, an explicit flag is a usage error while the
// default is silently dropped.
func placeBench(files FS, path, bench string, benchSet bool, act actions) (string, error) {
	bench = strings.TrimSpace(bench)
	if bench == "" && !benchSet {
		bench = defaultBenchDir
	}
	rel, err := relativeSub("--bench-dir", bench)
	if err != nil {
		return "", err
	}
	if !act.usesBench() {
		if benchSet {
			return "", usagef("benchmark: --bench-dir is only used when creating or probing")
		}
		return "", nil
	}
	return emptyDir(files, filepath.Join(path, rel), "--bench-dir")
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
	// The run removes the runtime dir at the end, so an existing one
	// must be an empty directory; emptyDir also covers stat failures
	// and a non-directory at that location.
	abs, err = emptyDir(files, abs, "--runtime-dir")
	if err != nil {
		return "", false, err
	}
	return abs, false, nil
}

// placeSrc validates the relative --src name under the runtime dir.
// The directory itself is created during the run, so nothing is
// stat'ed here. When the run creates no image, an explicit flag is a
// usage error while the default is silently dropped.
func placeSrc(src string, srcSet, create bool) (string, error) {
	src = strings.TrimSpace(src)
	if src == "" && !srcSet {
		src = payloadDir
	}
	rel, err := relativeSub("--src", src)
	if err != nil {
		return "", err
	}
	if !create {
		if srcSet {
			return "", usagef("benchmark: --src is only valid when creating an image")
		}
		return "", nil
	}
	return rel, nil
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
