package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"os"
	"strings"
	"testing"
)

func TestCacheAdviceShapes(t *testing.T) {
	numbers := func(randAvg float64) *PerfNumbers {
		return &PerfNumbers{SeqMiBps: 100, SeqAvgMs: 160, RandAvgMs: randAvg, RandMaxMs: randAvg * 2}
	}
	tests := []struct {
		name         string
		images       *PerfNumbers
		cache        *PerfNumbers
		wantStatus   string
		wantContains []string
		wantExcludes []string
	}{
		{
			name:         "both sides missing cannot compare",
			wantStatus:   statusInfo,
			wantContains: []string{"no storage comparison"},
		},
		{
			name:         "missing images side cannot compare",
			cache:        numbers(0.3),
			wantStatus:   statusInfo,
			wantContains: []string{"no storage comparison"},
		},
		{
			name:         "missing cache side cannot compare",
			images:       numbers(12),
			wantStatus:   statusInfo,
			wantContains: []string{"no storage comparison"},
		},
		{
			name:         "fast image storage keeps the direct mount",
			images:       numbers(0.5),
			cache:        numbers(0.3),
			wantStatus:   statusInfo,
			wantContains: []string{"direct mount is fine"},
		},
		{
			name:       "millisecond random reads against a fast cache warn",
			images:     numbers(12),
			cache:      numbers(1),
			wantStatus: statusWarn,
			wantContains: []string{
				"trend reference",
				"12.0x the cache directory",
				"cache_enabled",
			},
		},
		{
			name:         "baseline boundary warns",
			images:       numbers(5),
			cache:        numbers(2),
			wantStatus:   statusWarn,
			wantContains: []string{"5.00 ms", "cache_enabled"},
		},
		{
			name:         "below the baseline stays quiet",
			images:       numbers(4.9),
			cache:        numbers(0.1),
			wantStatus:   statusInfo,
			wantContains: []string{"direct mount is fine"},
		},
		{
			name:         "slow but close to the cache stays quiet",
			images:       numbers(12),
			cache:        numbers(10),
			wantStatus:   statusInfo,
			wantContains: []string{"direct mount is fine"},
		},
		{
			name:         "zero cache latency skips the ratio wording",
			images:       numbers(6),
			cache:        numbers(0),
			wantStatus:   statusWarn,
			wantContains: []string{"cache_enabled"},
			wantExcludes: []string{"x the cache directory"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cacheAdvice(tt.images, tt.cache)
			if got.Name != nameCacheAdvice {
				t.Fatalf("name = %s, want %s", got.Name, nameCacheAdvice)
			}
			if got.Status != tt.wantStatus {
				t.Fatalf("status = %s, want %s (%s)", got.Status, tt.wantStatus, got.Detail)
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(got.Detail, want) {
					t.Fatalf("detail = %q, want it to contain %q", got.Detail, want)
				}
			}
			for _, exclude := range tt.wantExcludes {
				if strings.Contains(got.Detail, exclude) {
					t.Fatalf("detail = %q, want it to exclude %q", got.Detail, exclude)
				}
			}
		})
	}
}

func TestMeasureDirMapsOutcomes(t *testing.T) {
	measured := PerfNumbers{SeqMiBps: 900, SeqAvgMs: 18.2, RandAvgMs: 0.5, RandMaxMs: 1.25}
	tests := []struct {
		name        string
		dir         string
		measure     func(context.Context, string) (PerfNumbers, error)
		wantStatus  string
		wantDetail  string
		wantNumbers bool
	}{
		{
			name: "measured directory summarizes the numbers",
			dir:  "/store/images",
			measure: func(context.Context, string) (PerfNumbers, error) {
				return measured, nil
			},
			wantStatus:  statusInfo,
			wantDetail:  "seq 900.0 MiB/s in 18.2 ms; rand avg 0.50 ms max 1.25 ms",
			wantNumbers: true,
		},
		{
			name: "unconfigured directory is skipped",
			dir:  "",
			measure: func(context.Context, string) (PerfNumbers, error) {
				t.Fatal("measure must not run for an unconfigured directory")
				return PerfNumbers{}, nil
			},
			wantStatus: statusInfo,
			wantDetail: "not configured, skipped",
		},
		{
			name: "missing directory is skipped",
			dir:  "/missing/images",
			measure: func(context.Context, string) (PerfNumbers, error) {
				return PerfNumbers{}, fmt.Errorf("create probe: %w", fs.ErrNotExist)
			},
			wantStatus: statusInfo,
			wantDetail: "not present, skipped",
		},
		{
			name: "unsupported platform degrades to info",
			dir:  "/store/images",
			measure: func(context.Context, string) (PerfNumbers, error) {
				return PerfNumbers{}, ErrPerfUnavailable
			},
			wantStatus: statusInfo,
			wantDetail: "performance probing requires linux",
		},
		{
			name: "io error becomes a warn carrying the text",
			dir:  "/locked/images",
			measure: func(context.Context, string) (PerfNumbers, error) {
				return PerfNumbers{}, fmt.Errorf("write probe: %w", os.ErrPermission)
			},
			wantStatus: statusWarn,
			wantDetail: "write probe: permission denied",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seams := Seams{Measure: tt.measure}
			result, numbers := measureDir(context.Background(), seams, namePerfImages, tt.dir)
			if result.Status != tt.wantStatus {
				t.Fatalf("status = %s, want %s (%s)", result.Status, tt.wantStatus, result.Detail)
			}
			if result.Detail != tt.wantDetail {
				t.Fatalf("detail = %q, want %q", result.Detail, tt.wantDetail)
			}
			if tt.wantNumbers {
				if numbers == nil || *numbers != measured {
					t.Fatalf("numbers = %+v, want %+v", numbers, measured)
				}
				if result.SeqReadMiBps == nil || *result.SeqReadMiBps != measured.SeqMiBps ||
					result.SeqReadAvgMs == nil || *result.SeqReadAvgMs != measured.SeqAvgMs ||
					result.RandReadAvgMs == nil || *result.RandReadAvgMs != measured.RandAvgMs ||
					result.RandReadMaxMs == nil || *result.RandReadMaxMs != measured.RandMaxMs {
					t.Fatalf("structured fields = %+v, want the measured numbers", result)
				}
				return
			}
			if numbers != nil {
				t.Fatalf("numbers = %+v, want nil", numbers)
			}
			if result.SeqReadMiBps != nil || result.SeqReadAvgMs != nil ||
				result.RandReadAvgMs != nil || result.RandReadMaxMs != nil {
				t.Fatalf("structured fields set without a measurement: %+v", result)
			}
		})
	}
}

func TestProbePerfAppendsAdviceAfterBothDirectories(t *testing.T) {
	host := healthyHost("0.5.0")
	host.truncate = func(string, int64) error { return nil }
	host.measure = func(_ context.Context, dir string) (PerfNumbers, error) {
		if dir == "/store/images" {
			return PerfNumbers{SeqMiBps: 100, SeqAvgMs: 160, RandAvgMs: 12, RandMaxMs: 30}, nil
		}
		return PerfNumbers{SeqMiBps: 1200, SeqAvgMs: 13, RandAvgMs: 0.4, RandMaxMs: 1}, nil
	}
	opts := Options{ImagesDir: "/store/images", CacheDir: "/store/cache"}
	results, err := Probe(context.Background(), host.seams(), opts)
	if err != nil {
		t.Fatal(err)
	}
	perf := results[len(results)-5 : len(results)-2]
	for i, want := range []string{namePerfImages, namePerfCache, nameCacheAdvice} {
		if perf[i].Name != want {
			t.Fatalf("result %d = %s, want %s", i, perf[i].Name, want)
		}
	}
	if perf[2].Status != statusWarn {
		t.Fatalf("advice status = %s, want warn (%s)", perf[2].Status, perf[2].Detail)
	}
}

func TestProbePerfCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	host := healthyHost("0.5.0")
	_, err := probePerf(ctx, host.seams(), Options{ImagesDir: "/img"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("probePerf err = %v, want context canceled", err)
	}
}

func TestPerfMeasureBoundedRunCleansUp(t *testing.T) {
	dir := t.TempDir()
	evictions := 0
	evict := func(*os.File, int64, int64) error { evictions++; return nil }
	numbers, err := perfMeasure(dir, 1<<20, evict)
	if err != nil {
		t.Fatal(err)
	}
	if numbers.SeqMiBps <= 0 || numbers.SeqAvgMs <= 0 ||
		numbers.RandAvgMs <= 0 || numbers.RandMaxMs <= 0 {
		t.Fatalf("numbers = %+v, want all positive", numbers)
	}
	if evictions != perfRandomReads+1 {
		t.Fatalf("evictions = %d, want %d", evictions, perfRandomReads+1)
	}
	assertDirEmpty(t, dir)
}

func TestPerfMeasureEvictFailureStillCleansUp(t *testing.T) {
	dir := t.TempDir()
	evict := func(*os.File, int64, int64) error { return errors.New("evict refused") }
	if _, err := perfMeasure(dir, 1<<20, evict); err == nil {
		t.Fatal("perfMeasure succeeded, want the evict failure")
	}
	assertDirEmpty(t, dir)
}

func TestWriteProbeContentIsDeterministic(t *testing.T) {
	var first, second bytes.Buffer
	writeOne := func(w *bytes.Buffer) {
		rng := rand.New(rand.NewSource(perfRandSeed))
		if err := writeProbeContent(w, 3<<20, rng); err != nil {
			t.Fatal(err)
		}
	}
	writeOne(&first)
	writeOne(&second)
	if first.Len() != 3<<20 {
		t.Fatalf("content size = %d, want %d", first.Len(), 3<<20)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("same seed produced different probe content")
	}
	if bytes.Equal(first.Bytes(), make([]byte, 3<<20)) {
		t.Fatal("probe content is all zeros, want pseudo-random bytes")
	}
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe file left behind: %v", entries)
	}
}
