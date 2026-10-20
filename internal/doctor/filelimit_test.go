package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestFormatSplitSize(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{2 << 30, "2G"},
		{64 << 10, "64K"},
		{1500, "1500"},
		{1 << 20, "1M"},
	}
	for _, tt := range tests {
		if got := formatSplitSize(tt.n); got != tt.want {
			t.Errorf("formatSplitSize(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestProbeFileLimitDegradesWithoutConclusion(t *testing.T) {
	tests := []struct {
		name       string
		dir        string
		probe      func(string, int64) error
		wantStatus string
		wantDetail string
	}{
		{
			name:       "empty directory is not configured",
			dir:        "",
			probe:      nil,
			wantStatus: statusInfo,
			wantDetail: "not configured, skipped",
		},
		{
			name: "missing directory is skipped",
			dir:  "/missing/images",
			probe: func(string, int64) error {
				return fmt.Errorf("create temp: %w", fs.ErrNotExist)
			},
			wantStatus: statusInfo,
			wantDetail: "not present, skipped",
		},
		{
			name: "permission error becomes a warn carrying the text",
			dir:  "/locked/images",
			probe: func(string, int64) error {
				return fmt.Errorf("create temp: %w", os.ErrPermission)
			},
			wantStatus: statusWarn,
			wantDetail: "create temp: permission denied",
		},
		{
			name:       "no limit below the cap passes",
			dir:        "/healthy/images",
			probe:      func(string, int64) error { return nil },
			wantStatus: statusPass,
			wantDetail: "no single-file size limit below 512 GiB",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe := tt.probe
			if probe == nil {
				probe = func(string, int64) error {
					t.Fatal("probe must not run for an unconfigured directory")
					return nil
				}
			}
			got := probeFileLimit(nameFileLimitImages, tt.dir, Options{}, probe)
			if got.Status != tt.wantStatus {
				t.Fatalf("status = %s, want %s (%s)", got.Status, tt.wantStatus, got.Detail)
			}
			if got.Detail != tt.wantDetail {
				t.Fatalf("detail = %q, want %q", got.Detail, tt.wantDetail)
			}
			if got.LimitBytes != nil {
				t.Fatalf("LimitBytes = %d, want unset", *got.LimitBytes)
			}
		})
	}
}

func TestProbeAppendsFileLimitResultsAfterSubdir(t *testing.T) {
	host := healthyHost("0.5.0")
	host.truncate = func(string, int64) error { return nil }
	opts := Options{ImagesDir: "/store/images", CacheDir: "/store/cache"}
	results, err := Probe(context.Background(), host.seams(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != probeCount() {
		t.Fatalf("probe count = %d, want %d", len(results), probeCount())
	}
	imagesAt := indexOf(t, results, nameFileLimitImages)
	if results[imagesAt-1].Name != nameSquashfuseSub {
		t.Fatalf("probe before file limits = %s, want %s",
			results[imagesAt-1].Name, nameSquashfuseSub)
	}
	limits := results[imagesAt : imagesAt+2]
	for i, want := range []struct {
		name string
		dir  string
	}{{nameFileLimitImages, "/store/images"}, {nameFileLimitCache, "/store/cache"}} {
		if limits[i].Name != want.name {
			t.Fatalf("result %d = %s, want %s", i, limits[i].Name, want.name)
		}
		if limits[i].Status != statusPass {
			t.Fatalf("%s status = %s, want pass (%s)",
				limits[i].Name, limits[i].Status, limits[i].Detail)
		}
	}
	if !host.truncateCalled("/store/images") || !host.truncateCalled("/store/cache") {
		t.Fatalf("truncate calls = %v, want both configured directories", host.truncateCalls)
	}
}

func TestTruncateProbeRemovesEveryProbeFile(t *testing.T) {
	dir := t.TempDir()
	for _, size := range []int64{1 << 20, 2 << 20, 4 << 20} {
		if err := truncateProbe(dir, size); err != nil {
			t.Fatalf("truncateProbe(%d): %v", size, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("probe file left behind at size %d: %v", size, entries)
		}
	}
}

func TestTruncateProbeMissingDirectoryReportsNotExist(t *testing.T) {
	err := truncateProbe(filepath.Join(t.TempDir(), "missing"), 1<<20)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("truncateProbe err = %v, want not exist", err)
	}
}

func TestProbeFileLimitCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	host := healthyHost("0.5.0")
	_, err := probeFileLimits(ctx, host.seams(), Options{ImagesDir: "/img"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("probeFileLimits err = %v, want context canceled", err)
	}
}
