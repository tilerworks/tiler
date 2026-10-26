package benchmark

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	runtimeMount = "mount"
	runtimeLogs  = "logs"
)

// DefaultPayloadDir is the --src name used when the flag was not set
// explicitly. Plan validates it as a relative name and the run
// creates it under the runtime dir; the CLI registers it as the
// cobra flag default so help and Plan stay in sync.
const DefaultPayloadDir = "data"

var imageNames = map[string]string{
	"dmg":      "image.dmg",
	"tar":      "image.tar.gz",
	"squashfs": "image.sqsh",
	"erofs":    "image.erofs",
}

// runState tracks the directories one run prepared. keep mirrors
// --keep: when true, cleanup and the abort path leave the runtime
// and bench dirs alone. src is the absolute payload dir under the
// runtime dir and is empty when the run creates no image.
type runState struct {
	bench   string
	runtime string
	keep    bool
	src     string
	mount   string
	logs    string
	image   string
	mounted *Mount
}

func prepareRun(d Decision) (*runState, error) {
	bench, err := prepareBench(d)
	if err != nil {
		return nil, err
	}
	state := &runState{
		bench: bench,
		keep:  d.Keep,
	}
	runtimeDir, err := prepareRuntime(d)
	if err != nil {
		return state.abort(err)
	}
	state.runtime = runtimeDir
	if d.Src != "" {
		state.src = filepath.Join(runtimeDir, d.Src)
	}
	mount := filepath.Join(runtimeDir, runtimeMount)
	logs := filepath.Join(runtimeDir, runtimeLogs)
	if err := os.MkdirAll(mount, 0o755); err != nil {
		return state.abort(fmt.Errorf("benchmark: mkdir %s: %w", mount, err))
	}
	state.mount = mount
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return state.abort(fmt.Errorf("benchmark: mkdir %s: %w", logs, err))
	}
	state.logs = logs
	if err := requireEmptyDir(mount, "mount point"); err != nil {
		return state.abort(err)
	}
	image, err := resolveImage(d, bench)
	if err != nil {
		return state.abort(err)
	}
	state.image = image
	return state, nil
}

// abort releases the prepared directories, then returns the prepare
// error unchanged.
func (s *runState) abort(err error) (*runState, error) {
	s.release()
	return nil, err
}

// release removes the runtime dir (owned or user-supplied) and the
// bench dir this run prepared, unless --keep asked to preserve them.
// A nil state releases nothing. It never touches the benchmark path
// itself or anything else under it, so abort and cleanup share
// exactly the same conditions.
func (s *runState) release() {
	if s == nil || s.keep {
		return
	}
	if s.runtime != "" {
		_ = os.RemoveAll(s.runtime)
	}
	if s.bench != "" {
		_ = os.RemoveAll(s.bench)
	}
}

// prepareBench creates the bench dir Plan joined under the benchmark
// path when the run creates an image or probes. Read-only image runs
// use no bench dir and adopt none.
func prepareBench(d Decision) (string, error) {
	if !d.Create && !d.Probe {
		return "", nil
	}
	if err := os.MkdirAll(d.BenchDir, 0o755); err != nil {
		return "", fmt.Errorf("benchmark: mkdir %s: %w", d.BenchDir, err)
	}
	return d.BenchDir, nil
}

func prepareRuntime(d Decision) (string, error) {
	if !d.OwnRuntime {
		if err := os.MkdirAll(d.RuntimeDir, 0o755); err != nil {
			return "", fmt.Errorf("benchmark: mkdir %s: %w", d.RuntimeDir, err)
		}
		// Plan already refuses a non-empty --runtime-dir; this guards
		// callers that build a Decision directly, because the run
		// removes the whole directory at the end.
		if err := requireEmptyDir(d.RuntimeDir, "--runtime-dir"); err != nil {
			return "", err
		}
		return d.RuntimeDir, nil
	}
	dir, err := os.MkdirTemp("", "tiler-bench-runtime-*")
	if err != nil {
		return "", fmt.Errorf("benchmark: mkdir runtime: %w", err)
	}
	return dir, nil
}

func resolveImage(d Decision, bench string) (string, error) {
	if d.Image != "" {
		return d.Image, nil
	}
	name, ok := imageNames[d.Type]
	if !ok {
		return "", fmt.Errorf("benchmark: unknown image type %q", d.Type)
	}
	if bench == "" {
		return "", fmt.Errorf("benchmark: bench directory is required to create an image")
	}
	return filepath.Join(bench, name), nil
}

// requireEmptyDir reports a runtime error when path holds entries.
// what names the directory kind in the message, such as "mount
// point" or "--runtime-dir".
func requireEmptyDir(path, what string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("benchmark: read %s: %w", path, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("benchmark: %s %s is not empty", what, path)
	}
	return nil
}

// cleanup detaches an active mount first, then releases the runtime
// and bench dirs under the same keep-based conditions as the abort
// path. The mount/ and logs/ subdirs go with the runtime dir; the
// benchmark path itself is never touched.
func (s *runState) cleanup(cmd Command) {
	if s != nil && s.mounted != nil {
		_ = Detach(cmd, *s.mounted, s.mount)
		s.mounted = nil
	}
	s.release()
}
