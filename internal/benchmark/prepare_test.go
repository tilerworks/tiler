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
		OwnRuntime: true,
		Type:       "nope",
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

func TestPrepareRunDropsCreatedBenchKeepsUserRuntime(t *testing.T) {
	vol := t.TempDir()
	bench := filepath.Join(vol, "bench")
	runtimeDir := t.TempDir()
	mount := filepath.Join(runtimeDir, "mount")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mount, "busy"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(runtimeDir, "keep-me")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Decision{
		Create:     true,
		Path:       vol,
		BenchDir:   bench,
		RuntimeDir: runtimeDir,
		OwnRuntime: false,
		Type:       "tar",
		Keep:       false,
	}
	_, err := prepareRun(d)
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(bench); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("bench = %v", statErr)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("marker = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(mount, "busy")); statErr != nil {
		t.Fatalf("busy = %v", statErr)
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
	if extra := newRuntimeTemps(before, runtimeTempNames(t)); len(extra) != 0 {
		t.Fatalf("runtime leaked: %v", extra)
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
