package doctor

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

const (
	nameFileLimitImages = "file_limit_images"
	nameFileLimitCache  = "file_limit_cache"

	// ladderStart is the first sparse size tested. A filesystem that
	// refuses it has a limit below the first rung.
	ladderStart = int64(4) << 30
	// ladderCap is the last sparse size tested. Accepting it means no
	// limit worth reporting.
	ladderCap = int64(512) << 30
	// probeFilePattern names the sparse probe files so leftovers are
	// recognizable.
	probeFilePattern = "tiler-doctor-*"

	splitGuidanceText = "enable split storage (blocks or members) in the configuration"
)

// truncateProbe creates a sparse probe file in dir and extends it to
// size without writing data, then removes it. Every path removes the
// probe file, so a refused truncate leaves no residue. Pure stdlib
// os calls, so the real implementation is platform independent.
func truncateProbe(dir string, size int64) error {
	file, err := os.CreateTemp(dir, probeFilePattern)
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	if err := file.Truncate(size); err != nil {
		closeErr := file.Close()
		return errors.Join(err, closeErr)
	}
	return file.Close()
}

// probeFileLimits reports the single-file size limit of the image
// library and the cache directory, in that order.
func probeFileLimits(ctx context.Context, seams Seams, opts Options) ([]Result, error) {
	if err := contextErr(ctx, nil); err != nil {
		return nil, err
	}
	return []Result{
		probeFileLimit(nameFileLimitImages, opts.ImagesDir, opts, seams.TruncateProbe),
		probeFileLimit(nameFileLimitCache, opts.CacheDir, opts, seams.TruncateProbe),
	}, nil
}

// probeFileLimit walks the sparse ladder for one directory and maps
// the outcome to a result. A found limit guides the user toward
// split storage; the probe never changes configuration itself. A
// missing directory or an unclassified IO error degrades the probe
// instead of failing the whole doctor run.
func probeFileLimit(name, dir string, opts Options, probe func(string, int64) error) Result {
	if dir == "" {
		return info(name, "not configured, skipped")
	}
	accepted, unlimited, err := fileLimitLadder(dir, probe)
	switch {
	case err != nil:
		if errors.Is(err, fs.ErrNotExist) {
			return info(name, "not present, skipped")
		}
		return warn(name, err.Error())
	case unlimited:
		return pass(name, "no single-file size limit below "+formatGiB(ladderCap))
	}
	result := limitResult(name, accepted, opts)
	if accepted > 0 {
		limit := accepted
		result.LimitBytes = &limit
	}
	return result
}

// fileLimitLadder doubles the sparse size from ladderStart to
// ladderCap. A size-class refusal ends the walk with the largest
// accepted size; the limit lies between that rung and the refused
// one. Any other error aborts the walk.
func fileLimitLadder(dir string, probe func(string, int64) error) (accepted int64, unlimited bool, err error) {
	for size := ladderStart; size <= ladderCap; size *= 2 {
		runErr := probe(dir, size)
		if runErr == nil {
			accepted = size
			continue
		}
		if isSizeLimitErr(runErr) {
			return accepted, false, nil
		}
		return 0, false, runErr
	}
	return accepted, true, nil
}

// limitResult maps a found limit onto the split-storage settings:
// a disabled split earns advisory guidance, an enabled split is
// already the recommended shape.
func limitResult(name string, accepted int64, opts Options) Result {
	var detail string
	if accepted > 0 {
		detail = fmt.Sprintf("single-file size limit between %s and %s",
			formatGiB(accepted), formatGiB(accepted*2))
	} else {
		detail = "single-file size limit below " + formatGiB(ladderStart)
	}
	if opts.SplitEnabled {
		return info(name, detail+"; split storage ("+opts.SplitMode+") is enabled")
	}
	return warn(name, detail+"; "+splitGuidanceText)
}

// formatGiB renders a byte size that is a whole number of GiB.
func formatGiB(size int64) string {
	return fmt.Sprintf("%d GiB", size>>30)
}
