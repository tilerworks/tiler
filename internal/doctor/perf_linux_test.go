//go:build linux && (amd64 || arm64 || loong64 || mips64 || mips64le || ppc64 || ppc64le || riscv64 || s390x)

package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMeasurePerfRealRunProducesPositiveNumbers(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	numbers, err := measurePerf(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if numbers.SeqMiBps <= 0 || numbers.SeqAvgMs <= 0 ||
		numbers.RandAvgMs <= 0 || numbers.RandMaxMs <= 0 {
		t.Fatalf("numbers = %+v, want all positive", numbers)
	}
	if numbers.RandMaxMs < numbers.RandAvgMs {
		t.Fatalf("max %v below avg %v", numbers.RandMaxMs, numbers.RandAvgMs)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe file left behind: %v", entries)
	}
}

func TestMeasurePerfMissingDirectoryReportsNotExist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := measurePerf(ctx, filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("measurePerf err = %v, want not exist", err)
	}
}

func TestMeasurePerfCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := measurePerf(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("measurePerf err = %v, want context canceled", err)
	}
}

func TestFadviseEvictAcceptsWholeFileAndWindow(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "evict-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := file.Truncate(perfReadWindow * 2); err != nil {
		t.Fatal(err)
	}
	if err := fadviseEvict(file, 0, 0); err != nil {
		t.Fatalf("whole-file evict: %v", err)
	}
	if err := fadviseEvict(file, 0, perfReadWindow); err != nil {
		t.Fatalf("window evict: %v", err)
	}
}
