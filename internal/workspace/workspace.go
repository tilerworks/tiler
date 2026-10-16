// Package workspace mounts image chains as writable merged views for
// incremental builds and tests. It is an internal helper: no cobra
// command is registered here, and no session record is written.
package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
)

// ErrUnavailable means mounting cannot work here: the OS is not
// Linux, or /dev/fuse and the FUSE tools cannot be used. Callers
// treat it as a skip.
var ErrUnavailable = errors.New("workspace: mounting requires linux and a usable /dev/fuse")

// Request describes one workspace mount.
type Request struct {
	// Full is the full image file mounted as the base layer.
	Full string
	// Layers lists incremental image files in chain order, oldest
	// first. Mounting reverses them so the newest layer wins.
	Layers []string
	// RuntimeDir is the configured runtime directory. Read-only
	// points and the upper and work directories live under it.
	RuntimeDir string
	// MergedDir is where the writable merged view is mounted.
	MergedDir string
	// Run executes external tools. Nil runs them through exec.
	Run func(name string, args ...string) error
}

// Workspace is a mounted merged view and its runtime state.
type Workspace struct {
	// MergedDir is the writable view mount point.
	MergedDir string
	// UpperDir holds the changes an incremental build packs.
	UpperDir string

	runtimeDir string
	mounts     []string
	run        func(name string, args ...string) error
}

// Mount mounts the chain as a writable merged view.
//
// Each squashfs is mounted read-only with squashfuse_ll under the
// runtime directory, newest increment first and the full image
// last, passing the real library path as the FUSE source name so
// the mount table can identify the image. fuse-overlayfs then
// merges them with the upper and work directories under
// runtime/up-<dev>-<ino>/. A failing mount unmounts every point
// this call created. Non-Linux hosts and a missing /dev/fuse return
// ErrUnavailable.
func Mount(req Request) (*Workspace, error) {
	run := req.Run
	if run == nil {
		run = runTool
	}
	return mount(req, run)
}

func mount(req Request, run func(name string, args ...string) error) (*Workspace, error) {
	if err := available(); err != nil {
		return nil, err
	}
	if req.Full == "" || req.RuntimeDir == "" || req.MergedDir == "" {
		return nil, errors.New("workspace: full image, runtime dir, and merged dir are required")
	}
	for _, dir := range []string{req.RuntimeDir, req.MergedDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("workspace: %w", err)
		}
	}
	dev, ino, err := devIno(req.MergedDir)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	w := &Workspace{
		MergedDir:  req.MergedDir,
		UpperDir:   filepath.Join(req.RuntimeDir, config.RuntimeDirName(dev, ino), "upper"),
		runtimeDir: filepath.Join(req.RuntimeDir, config.RuntimeDirName(dev, ino)),
		run:        run,
	}
	if err := os.MkdirAll(w.UpperDir, 0o755); err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(w.runtimeDir, "work"), 0o755); err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	if err := w.mountLowers(req); err != nil {
		return nil, err
	}
	if err := w.mountMerged(req); err != nil {
		return nil, err
	}
	return w, nil
}

// mountLowers mounts every squashfs read-only, newest layer first
// and the full image last, and records the points in lowerdir order.
func (w *Workspace) mountLowers(req Request) error {
	sources := make([]string, 0, len(req.Layers)+1)
	for i := len(req.Layers) - 1; i >= 0; i-- {
		sources = append(sources, req.Layers[i])
	}
	sources = append(sources, req.Full)
	for i, src := range sources {
		point := filepath.Join(w.runtimeDir, fmt.Sprintf("lower-%d", i))
		if err := os.MkdirAll(point, 0o755); err != nil {
			w.cleanup()
			return fmt.Errorf("workspace: %w", err)
		}
		// fsname keeps the real library path in the mount table, so
		// an identity lookup can name the image behind the view.
		if err := w.run("squashfuse_ll", "-o", "fsname="+src, src, point); err != nil {
			w.cleanup()
			return fmt.Errorf("workspace: %w", err)
		}
		w.mounts = append(w.mounts, point)
	}
	return nil
}

func (w *Workspace) mountMerged(req Request) error {
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s",
		strings.Join(w.mounts, ":"),
		w.UpperDir,
		filepath.Join(w.runtimeDir, "work"))
	if err := w.run("fuse-overlayfs", "-o", opts, req.MergedDir); err != nil {
		w.cleanup()
		return fmt.Errorf("workspace: %w", err)
	}
	w.mounts = append(w.mounts, req.MergedDir)
	return nil
}

// Unmount detaches every mount in reverse order with fusermount3.
func (w *Workspace) Unmount() error {
	var errs []error
	for i := len(w.mounts) - 1; i >= 0; i-- {
		if err := w.run("fusermount3", "-u", w.mounts[i]); err != nil {
			errs = append(errs, err)
		}
	}
	w.mounts = nil
	return errors.Join(errs...)
}

func (w *Workspace) cleanup() {
	_ = w.Unmount()
}

// SmokeReadOnly mounts one squashfs file read-only, lists its root,
// and unmounts it again. Missing tools and a missing /dev/fuse
// return ErrUnavailable so callers can skip; a file that does not
// mount or read returns a real error.
func SmokeReadOnly(imagePath string) error {
	return smokeReadOnly(imagePath, runTool)
}

func smokeReadOnly(imagePath string, run func(name string, args ...string) error) error {
	if err := available(); err != nil {
		return err
	}
	for _, tool := range []string{"squashfuse_ll", "fusermount3"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%w: %s is missing", ErrUnavailable, tool)
		}
	}
	point, err := os.MkdirTemp("", "tiler-smoke-")
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(point) }()
	if err := run("squashfuse_ll", imagePath, point); err != nil {
		return fmt.Errorf("workspace: smoke mount: %w", err)
	}
	_, listErr := os.ReadDir(point)
	unmountErr := run("fusermount3", "-u", point)
	if listErr != nil {
		return fmt.Errorf("workspace: smoke read: %w", listErr)
	}
	if unmountErr != nil {
		return fmt.Errorf("workspace: smoke unmount: %w", unmountErr)
	}
	return nil
}

// runTool is the default runner. A non-zero exit carries the
// trimmed combined output.
func runTool(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, bytes.TrimSpace(out))
	}
	return nil
}
