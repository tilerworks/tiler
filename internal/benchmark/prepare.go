package benchmark

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

const (
	runtimeMount = "mount"
	runtimeLogs  = "logs"
	payloadDir   = "data"
)

var imageNames = map[string]string{
	"dmg":      "image.dmg",
	"tar":      "image.tar.gz",
	"squashfs": "image.sqsh",
	"erofs":    "image.erofs",
}

type runState struct {
	bench       string
	removeBench bool
	runtime     string
	ownRuntime  bool
	mount       string
	logs        string
	image       string
	mounted     *Mount
}

func prepareRun(d Decision) (*runState, error) {
	bench, removeBench, err := prepareBench(d)
	if err != nil {
		return nil, err
	}
	state := &runState{
		bench:       bench,
		removeBench: removeBench,
		ownRuntime:  d.OwnRuntime,
	}
	runtimeDir, err := prepareRuntime(d)
	if err != nil {
		return state.abort(err)
	}
	state.runtime = runtimeDir
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
	if err := requireEmptyDir(mount); err != nil {
		return state.abort(err)
	}
	image, err := resolveImage(d, bench)
	if err != nil {
		return state.abort(err)
	}
	state.image = image
	return state, nil
}

// abort removes an owned runtime and a bench this call would have
// removed, then returns the prepare error unchanged.
func (s *runState) abort(err error) (*runState, error) {
	s.releasePrepared()
	return nil, err
}

func (s *runState) releasePrepared() {
	if s == nil {
		return
	}
	if s.ownRuntime && s.runtime != "" {
		_ = os.RemoveAll(s.runtime)
	}
	if s.removeBench && s.bench != "" {
		_ = os.RemoveAll(s.bench)
	}
}

func prepareBench(d Decision) (string, bool, error) {
	if !d.Create && !d.Probe {
		return "", false, nil
	}
	bench := d.BenchDir
	if bench == "" {
		name, err := randomBenchName()
		if err != nil {
			return "", false, err
		}
		bench = filepath.Join(d.Path, name)
	}
	if err := os.MkdirAll(bench, 0o755); err != nil {
		return "", false, fmt.Errorf("benchmark: mkdir %s: %w", bench, err)
	}
	return bench, !d.Keep, nil
}

func prepareRuntime(d Decision) (string, error) {
	if !d.OwnRuntime {
		if err := os.MkdirAll(d.RuntimeDir, 0o755); err != nil {
			return "", fmt.Errorf("benchmark: mkdir %s: %w", d.RuntimeDir, err)
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

func randomBenchName() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("benchmark: random bench name: %w", err)
	}
	return fmt.Sprintf("tiler-bench-%x", b), nil
}

func requireEmptyDir(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("benchmark: read %s: %w", path, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("benchmark: mount point %s is not empty", path)
	}
	return nil
}

func (s *runState) cleanup(cmd Command) {
	if s == nil {
		return
	}
	if s.mounted != nil {
		_ = Detach(cmd, *s.mounted, s.mount)
		s.mounted = nil
	}
	if s.ownRuntime {
		_ = os.RemoveAll(s.runtime)
	} else {
		_ = os.RemoveAll(s.mount)
		_ = os.RemoveAll(s.logs)
	}
	if s.removeBench && s.bench != "" {
		_ = os.RemoveAll(s.bench)
	}
}
