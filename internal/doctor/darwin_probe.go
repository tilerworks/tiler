package doctor

import (
	"context"
	"fmt"

	"github.com/jiangxin/tiler/internal/container"
)

// probeDarwin is the macOS probe set. FUSE device and helper
// failures are omitted because the hot path uses hdiutil. Cold-read
// measurements and the contrib tools stay informational.
func probeDarwin(ctx context.Context, seams Seams, opts Options) ([]Result, error) {
	gitResult, err := probeDarwinGit(ctx, seams)
	if err != nil {
		return nil, err
	}
	toolchain, err := probeDarwinToolchain(ctx, seams)
	if err != nil {
		return nil, err
	}
	limits, err := probeFileLimits(ctx, seams, opts)
	if err != nil {
		return nil, err
	}
	perf, err := probeDarwinPerf(ctx, seams, opts)
	if err != nil {
		return nil, err
	}
	results := []Result{gitResult, toolchain}
	results = append(results, limits...)
	results = append(results, perf...)
	return append(results, probeDarwinContrib(seams)...), nil
}

func probeDarwinGit(ctx context.Context, seams Seams) (Result, error) {
	if err := contextErr(ctx, nil); err != nil {
		return Result{}, err
	}
	path, err := seams.LookPath("git")
	if err != nil {
		detail := fmt.Sprintf("git: %v; install: %s", err, gitDarwinInstall)
		return fail(nameGit, detail), nil
	}
	ver, detail, err := readVersion(ctx, seams, path, []string{"git"})
	if err != nil {
		return Result{}, err
	}
	if detail != "" {
		return fail(nameGit, detail), nil
	}
	return pass(nameGit, ver.raw), nil
}

// probeDarwinToolchain passes when /usr/bin/hdiutil can run. A
// missing binary tells the user to restore the system tool and does
// not suggest a package install. HFS+ is named when every new image
// uses that filesystem because shadow cannot attach an APFS image.
func probeDarwinToolchain(ctx context.Context, seams Seams) (Result, error) {
	if err := contextErr(ctx, nil); err != nil {
		return Result{}, err
	}
	if err := seams.Hdiutil(); err != nil {
		return fail(nameImageToolchain, hdiutilMissing), nil
	}
	if container.DMGCreateFilesystem() == "HFS+" {
		return pass(nameImageToolchain, dmgHFSDetail), nil
	}
	return pass(nameImageToolchain, "dmg"), nil
}

// probeDarwinPerf keeps the cold-read results and forces them to
// info. A measurement error must not fail doctor: the hot path does
// not use FUSE.
func probeDarwinPerf(ctx context.Context, seams Seams, opts Options) ([]Result, error) {
	results, err := probePerf(ctx, seams, opts)
	if err != nil {
		return nil, err
	}
	for i := range results {
		results[i].Status = statusInfo
	}
	return results, nil
}

// probeDarwinContrib reports concatfs and slowfs as info whether or
// not they are installed. They are not on the mount path.
func probeDarwinContrib(seams Seams) []Result {
	results := make([]Result, 0, len(contribTools))
	for _, tool := range contribTools {
		path, err := seams.LookPath(tool.binary)
		if err != nil {
			results = append(results, info(tool.name, contribGuidanceText))
			continue
		}
		result := info(tool.name, path)
		result.ToolPath = path
		results = append(results, result)
	}
	return results
}
