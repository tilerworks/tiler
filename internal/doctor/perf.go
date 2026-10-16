package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"time"
)

const (
	namePerfImages  = "storage_perf_images"
	namePerfCache   = "storage_perf_cache"
	nameCacheAdvice = "cache_advice"

	// perfProbeSize is the fixed probe file size. The measurement
	// stays in the seconds range with it.
	perfProbeSize = int64(16) << 20
	// perfReadWindow is the random-read window size.
	perfReadWindow = int64(4096)
	// perfRandomReads is the fixed count of random window reads.
	perfRandomReads = 64
	// perfRandSeed makes the probe content and the random offsets
	// deterministic so repeated runs compare the same bytes.
	perfRandSeed = int64(20260101)
	// perfProbePattern names the probe files so leftovers are
	// recognizable.
	perfProbePattern = "tiler-doctor-perf-*"

	// cacheAdviceBaselineMs is the random-read latency seen by
	// injected-latency experiments on git-style cold workloads: a
	// trend reference for the advice wording, not an SLA threshold.
	cacheAdviceBaselineMs = 5.0
	// cacheAdviceRatio is how much worse than the cache directory the
	// image storage must measure before the advice fires.
	cacheAdviceRatio = 2.0
)

// ErrPerfUnavailable reports that the platform cannot evict the page
// cache for a cold read, so performance probing degrades to info.
var ErrPerfUnavailable = errors.New("performance probing requires linux")

// PerfNumbers is one directory's cold-read measurement.
type PerfNumbers struct {
	SeqMiBps  float64
	SeqAvgMs  float64
	RandAvgMs float64
	RandMaxMs float64
}

// probePerf measures both storage directories and derives the cache
// advice from the two measurements.
func probePerf(ctx context.Context, seams Seams, opts Options) ([]Result, error) {
	if err := contextErr(ctx, nil); err != nil {
		return nil, err
	}
	images, imagesNumbers := measureDir(ctx, seams, namePerfImages, opts.ImagesDir)
	cache, cacheNumbers := measureDir(ctx, seams, namePerfCache, opts.CacheDir)
	return []Result{images, cache, cacheAdvice(imagesNumbers, cacheNumbers)}, nil
}

// measureDir runs one directory's measurement. It returns the result
// and the measured numbers; the numbers are nil when the directory
// produced no measurement (unconfigured, missing, unsupported
// platform, or IO error).
func measureDir(ctx context.Context, seams Seams, name, dir string) (Result, *PerfNumbers) {
	if dir == "" {
		return info(name, "not configured, skipped"), nil
	}
	numbers, err := seams.Measure(ctx, dir)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return info(name, "not present, skipped"), nil
		case errors.Is(err, ErrPerfUnavailable):
			return info(name, err.Error()), nil
		default:
			return warn(name, err.Error()), nil
		}
	}
	measured := numbers
	result := info(name, perfDetail(numbers))
	result.SeqReadMiBps = &measured.SeqMiBps
	result.SeqReadAvgMs = &measured.SeqAvgMs
	result.RandReadAvgMs = &measured.RandAvgMs
	result.RandReadMaxMs = &measured.RandMaxMs
	return result, &measured
}

// cacheAdvice compares the two measurements. Numbers are a trend
// reference, so the advice fires only when the image storage is both
// at or above the reference baseline and clearly worse than the
// cache directory. Any missing side leaves nothing to compare.
func cacheAdvice(images, cache *PerfNumbers) Result {
	if images == nil || cache == nil {
		return info(nameCacheAdvice,
			"no storage comparison without measurements from both directories")
	}
	img, local := images.RandAvgMs, cache.RandAvgMs
	if img >= cacheAdviceBaselineMs && img >= cacheAdviceRatio*local {
		detail := fmt.Sprintf(
			"image storage random read averages %.2f ms, at or above the %.0f ms trend reference",
			img, cacheAdviceBaselineMs)
		if local > 0 {
			detail += fmt.Sprintf(" and %.1fx the cache directory", img/local)
		}
		detail += "; evaluate cache_enabled to copy images into the faster local cache"
		return warn(nameCacheAdvice, detail)
	}
	return info(nameCacheAdvice,
		"direct mount is fine: image storage random read stays below the trend reference or close to the cache directory")
}

func perfDetail(n PerfNumbers) string {
	return fmt.Sprintf("seq %.1f MiB/s in %.1f ms; rand avg %.2f ms max %.2f ms",
		n.SeqMiBps, n.SeqAvgMs, n.RandAvgMs, n.RandMaxMs)
}

// perfMeasure writes a deterministic pseudo-random probe file of
// size bytes in dir, evicts it from the page cache through evict,
// and times one full sequential read plus the fixed count of random
// window reads. evict drops the cached pages of one region; the
// linux implementation uses fadvise. The probe file is removed on
// every path. size must be larger than the read window.
func perfMeasure(dir string, size int64, evict func(*os.File, int64, int64) error) (PerfNumbers, error) {
	file, err := os.CreateTemp(dir, perfProbePattern)
	if err != nil {
		return PerfNumbers{}, err
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	content := rand.New(rand.NewSource(perfRandSeed))
	if err := writeProbeContent(file, size, content); err != nil {
		closeErr := file.Close()
		return PerfNumbers{}, errors.Join(err, closeErr)
	}
	if err := file.Close(); err != nil {
		return PerfNumbers{}, err
	}
	reader, err := os.Open(name)
	if err != nil {
		return PerfNumbers{}, err
	}
	defer func() { _ = reader.Close() }()
	return timeReads(reader, size, evict)
}

// writeProbeContent fills w with deterministic pseudo-random bytes,
// refilling the write buffer on every chunk.
func writeProbeContent(w io.Writer, size int64, rng *rand.Rand) error {
	buffer := make([]byte, 1<<20)
	for written := int64(0); written < size; {
		chunk := int64(len(buffer))
		if remaining := size - written; remaining < chunk {
			chunk = remaining
		}
		_, _ = rng.Read(buffer[:chunk])
		n, err := w.Write(buffer[:chunk])
		written += int64(n)
		if err != nil {
			return err
		}
	}
	return nil
}

// timeReads evicts the whole file, times one full sequential pass,
// then the fixed count of random window reads, evicting each window
// before reading it cold.
func timeReads(f *os.File, size int64, evict func(*os.File, int64, int64) error) (PerfNumbers, error) {
	if err := evict(f, 0, 0); err != nil {
		return PerfNumbers{}, err
	}
	start := time.Now()
	read, err := io.Copy(io.Discard, f)
	elapsed := time.Since(start)
	if err != nil {
		return PerfNumbers{}, err
	}
	numbers := PerfNumbers{
		SeqMiBps: float64(read) / (1 << 20) / elapsed.Seconds(),
		SeqAvgMs: elapsed.Seconds() * 1000,
	}
	offsets := rand.New(rand.NewSource(perfRandSeed))
	window := make([]byte, perfReadWindow)
	var total time.Duration
	var longest time.Duration
	for range perfRandomReads {
		offset := offsets.Int63n(size - perfReadWindow)
		if err := evict(f, offset, perfReadWindow); err != nil {
			return PerfNumbers{}, err
		}
		readStart := time.Now()
		if _, err := f.ReadAt(window, offset); err != nil {
			return PerfNumbers{}, err
		}
		took := time.Since(readStart)
		total += took
		if took > longest {
			longest = took
		}
	}
	numbers.RandAvgMs = total.Seconds() * 1000 / perfRandomReads
	numbers.RandMaxMs = longest.Seconds() * 1000
	return numbers, nil
}
