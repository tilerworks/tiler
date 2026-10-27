package benchmark

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareRunRemovesOwnedRuntimeAndBench(t *testing.T) {
	vol := t.TempDir()
	before := runtimeTempNames(t)
	d := Decision{
		Create:     true,
		Path:       vol,
		BenchDir:   filepath.Join(vol, "tiler-bench"),
		OwnRuntime: true,
		Type:       "nope",
		Src:        "data",
		Keep:       false,
	}
	_, err := prepareRun(d)
	if err == nil {
		t.Fatal("prepareRun succeeded")
	}
	if !strings.Contains(err.Error(), `unknown image type "nope"`) {
		t.Fatalf("err = %v", err)
	}
	entries, readErr := os.ReadDir(vol)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("bench leaked: %v", entries)
	}
	if extra := newRuntimeTemps(before, runtimeTempNames(t)); len(extra) != 0 {
		t.Fatalf("runtime leaked: %v", extra)
	}
}

func TestPrepareRunRejectsNonEmptyRuntimeSubdir(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "tiler-bench")
	runtimeDir := t.TempDir()
	marker := filepath.Join(runtimeDir, "keep-me")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(runtimeDir, "runtime")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "old"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Decision{
		Create:     true,
		Path:       vol,
		BenchDir:   bench,
		RuntimeDir: sub,
		OwnRuntime: false,
		Type:       "tar",
		Src:        "data",
		Keep:       false,
	}
	_, err := prepareRun(d)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err = %v", err)
	}
	// The user runtime dir and everything under it stay untouched.
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("marker = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(sub, "old")); statErr != nil {
		t.Fatalf("subdir content = %v", statErr)
	}
	// The abort path removes the bench dir this run created.
	if _, statErr := os.Stat(bench); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("bench = %v", statErr)
	}
}

func TestPrepareRunKeepsExistingRuntimeParent(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "tiler-bench")
	runtimeDir := t.TempDir()
	marker := filepath.Join(runtimeDir, "keep-me")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Decision{
		Create:     true,
		Path:       vol,
		BenchDir:   bench,
		RuntimeDir: filepath.Join(runtimeDir, "runtime"),
		OwnRuntime: false,
		Type:       "tar",
		Src:        "data",
		Keep:       false,
	}
	state, err := prepareRun(d)
	if err != nil {
		t.Fatalf("prepareRun: %v", err)
	}
	if want := filepath.Join(runtimeDir, "runtime", "data"); state.src != want {
		t.Fatalf("src = %q, want %q", state.src, want)
	}
	if _, statErr := os.Stat(filepath.Join(runtimeDir, "runtime", "mount")); statErr != nil {
		t.Fatalf("mount = %v", statErr)
	}
	state.cleanup(func(string, []string, ...string) ([]byte, error) {
		t.Fatal("cleanup must not run a command")
		return nil, nil
	})
	// The pre-existing parent and its own content survive; only the
	// runtime subdir this run created is removed.
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("marker after cleanup = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(runtimeDir, "runtime")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("runtime subdir after cleanup = %v", statErr)
	}
	if _, statErr := os.Stat(bench); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("bench after cleanup = %v", statErr)
	}
	if entries := listNames(t, vol); len(entries) != 0 {
		t.Fatalf("path leftovers: %v", entries)
	}
}

func TestPrepareRunRemovesCreatedRuntimeParent(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "tiler-bench")
	runtimeDir := filepath.Join(t.TempDir(), "created")
	d := Decision{
		Create:            true,
		Path:              vol,
		BenchDir:          bench,
		RuntimeDir:        filepath.Join(runtimeDir, "runtime"),
		RuntimeParent:     runtimeDir,
		RuntimeDirCreated: true,
		OwnRuntime:        false,
		Type:              "tar",
		Src:               "data",
		Keep:              false,
	}
	state, err := prepareRun(d)
	if err != nil {
		t.Fatalf("prepareRun: %v", err)
	}
	if _, statErr := os.Stat(runtimeDir); statErr != nil {
		t.Fatalf("created parent = %v", statErr)
	}
	state.cleanup(func(string, []string, ...string) ([]byte, error) {
		t.Fatal("cleanup must not run a command")
		return nil, nil
	})
	// The run created the parent, cleaned its runtime subdir, and
	// removed the empty parent again.
	if _, statErr := os.Stat(runtimeDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("parent after cleanup = %v", statErr)
	}
	if _, statErr := os.Stat(bench); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("bench after cleanup = %v", statErr)
	}
}

func TestPrepareRunKeepsCreatedRuntimeParentWithForeignFile(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "tiler-bench")
	runtimeDir := filepath.Join(t.TempDir(), "created")
	d := Decision{
		Create:            true,
		Path:              vol,
		BenchDir:          bench,
		RuntimeDir:        filepath.Join(runtimeDir, "runtime"),
		RuntimeParent:     runtimeDir,
		RuntimeDirCreated: true,
		OwnRuntime:        false,
		Type:              "tar",
		Src:               "data",
		Keep:              false,
	}
	state, err := prepareRun(d)
	if err != nil {
		t.Fatalf("prepareRun: %v", err)
	}
	// Simulate a file the user drops into the parent while the run
	// is active: the parent is no longer empty after the runtime
	// subdir is cleaned, so it must survive.
	foreign := filepath.Join(runtimeDir, "foreign")
	if err := os.WriteFile(foreign, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	state.cleanup(func(string, []string, ...string) ([]byte, error) {
		t.Fatal("cleanup must not run a command")
		return nil, nil
	})
	if _, statErr := os.Stat(foreign); statErr != nil {
		t.Fatalf("foreign file = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(runtimeDir, "runtime")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("runtime subdir after cleanup = %v", statErr)
	}
}

func TestPrepareRunCreatesMissingPath(t *testing.T) {
	vol := filepath.Join(t.TempDir(), "new-vol")
	bench := filepath.Join(vol, "tiler-bench")
	d := Decision{
		Create:     true,
		Path:       vol,
		CreatePath: true,
		BenchDir:   bench,
		OwnRuntime: true,
		Type:       "tar",
		Src:        "data",
	}
	state, err := prepareRun(d)
	if err != nil {
		t.Fatalf("prepareRun: %v", err)
	}
	if _, statErr := os.Stat(vol); statErr != nil {
		t.Fatalf("created path = %v", statErr)
	}
	state.cleanup(func(string, []string, ...string) ([]byte, error) {
		t.Fatal("cleanup must not run a command")
		return nil, nil
	})
	// The path itself is never removed; only the bench dir goes.
	if _, statErr := os.Stat(vol); statErr != nil {
		t.Fatalf("path after cleanup = %v", statErr)
	}
	if _, statErr := os.Stat(bench); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("bench after cleanup = %v", statErr)
	}
}

func TestPrepareRunKeepsBenchWhenRequested(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "bench")
	before := runtimeTempNames(t)
	d := Decision{
		Create:     true,
		Path:       vol,
		BenchDir:   bench,
		OwnRuntime: true,
		Type:       "nope",
		Keep:       true,
	}
	_, err := prepareRun(d)
	if err == nil || !strings.Contains(err.Error(), `unknown image type "nope"`) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(bench); statErr != nil {
		t.Fatalf("kept bench = %v", statErr)
	}
	// Keep also preserves the owned runtime dir, so the test must
	// remove the leaked temp dir itself.
	extra := newRuntimeTemps(before, runtimeTempNames(t))
	if len(extra) != 1 {
		t.Fatalf("kept runtime temps = %v, want exactly 1", extra)
	}
	kept := filepath.Join(os.TempDir(), extra[0])
	defer func() { _ = os.RemoveAll(kept) }()
	if _, statErr := os.Stat(filepath.Join(kept, "mount")); statErr != nil {
		t.Fatalf("kept runtime mount = %v", statErr)
	}
}

func runtimeTempNames(t *testing.T) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]struct{})
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "tiler-bench-runtime-") {
			names[entry.Name()] = struct{}{}
		}
	}
	return names
}

func newRuntimeTemps(before, after map[string]struct{}) []string {
	var extra []string
	for name := range after {
		if _, ok := before[name]; !ok {
			extra = append(extra, name)
		}
	}
	return extra
}
