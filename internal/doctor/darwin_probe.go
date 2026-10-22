package doctor

import (
	"context"
	"fmt"

	"github.com/jiangxin/tiler/internal/container"
)

// probeDarwin is the macOS probe set. FUSE device and helper
// failures are omitted because the hot path uses hdiutil. Cold-read
// measurements stay informational. concatfs and slowfs stay in the
// report and state that macOS does not use them.
func probeDarwin(ctx context.Context, seams Seams, opts Options) ([]Result, error) {
	var images, cache *PerfNumbers
	tasks := []probeTask{
		{name: nameGit, run: func(ctx context.Context) (Result, error) {
			return probeDarwinGit(ctx, seams)
		}},
		{name: nameImageToolchain, run: func(ctx context.Context) (Result, error) {
			return probeDarwinToolchain(ctx, seams)
		}},
		fileLimitTask(nameFileLimitImages, opts.ImagesDir, opts, seams),
		fileLimitTask(nameFileLimitCache, opts.CacheDir, opts, seams),
		asInfo(perfTask(namePerfImages, opts.ImagesDir, seams, &images)),
		asInfo(perfTask(namePerfCache, opts.CacheDir, seams, &cache)),
	}
	tasks = append(tasks, contribTasks(seams, true)...)
	view := newProbeView(opts.Progress, probeNames(tasks))
	results, err := runProbeTasks(ctx, view, tasks)
	if err != nil {
		view.close()
		return nil, err
	}
	results, err = withAdvice(view, results, cacheAdvice(images, cache), true)
	view.close()
	return results, err
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

// darwinContribUnused is the detail for concatfs and slowfs on
// macOS. The names stay so the report matches Linux, and the text
// says the dmg path does not use these FUSE helpers. It does not
// repeat the Linux build guidance.
const darwinContribUnused = "unused on macOS; the dmg path does not use FUSE helpers"

// probeDarwinContribTool reports one helper as info and states that
// macOS does not use it. A resolved path is recorded after that
// note. A missing binary stays info and does not suggest a build.
func probeDarwinContribTool(seams Seams, name, binary string) Result {
	path, err := seams.LookPath(binary)
	if err != nil {
		return info(name, darwinContribUnused)
	}
	result := info(name, darwinContribUnused+"; "+path)
	result.ToolPath = path
	return result
}
