// Package doctor probes the local image toolchain and external tools.
// Command execution and stat go through seams so unit tests never
// touch host binaries. A failing probe is a diagnostic result. The
// only error is when the result set itself cannot be produced.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jiangxin/tiler/internal/container"
)

const (
	nameFuseDevice     = "fuse_device"
	nameFuseOverlayfs  = "fuse_overlayfs"
	nameFusermount3    = "fusermount3"
	nameGit            = "git"
	nameImageToolchain = "image_toolchain"
	nameSquashfuseSub  = "squashfuse_subdir"
	statusPass         = "pass"
	statusFail         = "fail"
	statusWarn         = "warn"
	statusInfo         = "info"
	fuseDevicePath     = "/dev/fuse"
	subdirBaselineText = "0.5.0"
	// profileDarwin selects the macOS probe set. The empty profile
	// stays the Linux FUSE set, including in unit tests.
	profileDarwin = "darwin"
	// darwinProbeCount is git, the hdiutil toolchain, two file-limit
	// probes, three storage probes, and two contrib probes.
	darwinProbeCount = 9
	hdiutilMissing   = "/usr/bin/hdiutil cannot run; restore the system hdiutil"
	dmgHFSDetail     = "dmg; filesystem HFS+ because attach -shadow cannot use an APFS image on this system"
	gitDarwinInstall = "xcode-select --install"
)

// Seams are the host operations a probe is allowed to perform.
// Tests replace them. Production fills them from the host package
// plus the stdlib truncate probe.
type Seams struct {
	LookPath func(name string) (string, error)
	Stat     func(name string) error
	Run      func(ctx context.Context, name string, args ...string) ([]byte, error)
	// TruncateProbe creates and removes a sparse file of the given
	// size in dir, returning the filesystem's verdict.
	TruncateProbe func(dir string, size int64) error
	// Measure times cold sequential and random reads of a probe
	// file in dir.
	Measure func(ctx context.Context, dir string) (PerfNumbers, error)
	// Profile selects the probe set. Empty is the Linux FUSE set.
	// profileDarwin checks /usr/bin/hdiutil and does not fail the
	// FUSE probes.
	Profile string
	// Hdiutil reports whether /usr/bin/hdiutil can run. The darwin
	// profile requires it. Production stats the path and does not
	// execute it. Tests inject present and missing.
	Hdiutil func() error
}

// Result is one probe line: a stable name, a status (pass, fail,
// warn, or info), and a detail that keeps the reason for a failure
// or summarizes a measurement. The optional structured fields carry
// measurements for machine consumers of the JSON rendering; the
// human rendering stays on the detail text. Fields are add-only.
type Result struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	// LimitBytes is a detected single-file size limit in bytes.
	LimitBytes *int64 `json:"limit_bytes,omitempty"`
	// SeqReadMiBps is sequential read throughput in MiB per second.
	SeqReadMiBps *float64 `json:"seq_read_mibps,omitempty"`
	// SeqReadAvgMs is the full sequential read latency in
	// milliseconds.
	SeqReadAvgMs *float64 `json:"seq_read_avg_ms,omitempty"`
	// RandReadAvgMs is average random read latency in milliseconds.
	RandReadAvgMs *float64 `json:"rand_read_avg_ms,omitempty"`
	// RandReadMaxMs is maximum random read latency in milliseconds.
	RandReadMaxMs *float64 `json:"rand_read_max_ms,omitempty"`
	// ToolPath is the resolved path of a probed external tool.
	ToolPath string `json:"tool_path,omitempty"`
}

// Options carries the configuration inputs the storage probes need:
// the directories to inspect and the split-storage settings that
// shape probe advice.
type Options struct {
	// ImagesDir is the image library directory to probe.
	ImagesDir string
	// CacheDir is the local cache directory to probe.
	CacheDir string
	// SplitSize is the configured block size in bytes. Zero means
	// split storage is off; a positive value shapes probe advice.
	SplitSize int64
}

type toolSpec struct {
	name   string
	binary string
	hints  []string
}

type parsedTool struct {
	result  Result
	version version
	ok      bool
}

// toolSpecs is the shared external-tool probe list. Image container
// binaries are not listed here; probeImageToolchain aggregates each
// Format.Tools stack. squashfuse_subdir is a version baseline, not a
// binary of its own.
var toolSpecs = []toolSpec{
	{name: nameFuseOverlayfs, binary: "fuse-overlayfs", hints: []string{"fuse-overlayfs", "fuse_overlayfs"}},
	{name: nameFusermount3, binary: "fusermount3", hints: []string{"fusermount3", "fusermount"}},
	{name: nameGit, binary: "git", hints: []string{"git"}},
}

// probeCount is the number of results the Linux FUSE profile
// returns: the FUSE device, each shared tool, the image toolchain
// aggregate, the squashfuse subdir baseline, two file-limit probes,
// three storage performance probes, and two contrib probes.
func probeCount() int {
	return 1 + len(toolSpecs) + 1 + 1 + 2 + 3 + 2
}

// listedProbeCount is the number of results Probe returns for these
// seams. The Linux set stays probeCount so tests that inject a fake
// host still expect the FUSE probes.
func (s Seams) listedProbeCount() int {
	if s.Profile == profileDarwin {
		return darwinProbeCount
	}
	return probeCount()
}

// Probe runs every environment probe. The empty profile is the
// Linux FUSE set: a missing shared tool, an unparsable banner, or
// an unavailable FUSE device becomes a fail result. Image container
// tools are one aggregate: at least one complete stack passes, a
// missing stack warns, and no complete stack fails image_toolchain.
// The darwin profile checks /usr/bin/hdiutil instead of those FUSE
// failures. The storage probes degrade to warn or info. The options
// carry the configured storage directories and split settings. An
// error is returned only when the set cannot be built, including a
// cancelled context or seams that were not supplied.
func Probe(ctx context.Context, seams Seams, opts Options) ([]Result, error) {
	if err := contextErr(ctx, nil); err != nil {
		return nil, err
	}
	if err := seams.ready(); err != nil {
		return nil, err
	}
	if seams.Profile == profileDarwin {
		return probeDarwin(ctx, seams, opts)
	}
	return probeFUSE(ctx, seams, opts)
}

func probeFUSE(ctx context.Context, seams Seams, opts Options) ([]Result, error) {
	device, err := probeDevice(ctx, seams)
	if err != nil {
		return nil, err
	}
	tools, err := probeTools(ctx, seams)
	if err != nil {
		return nil, err
	}
	toolchain, err := probeImageToolchain(ctx, seams)
	if err != nil {
		return nil, err
	}
	subdir, err := probeSquashfuseSubdir(ctx, seams)
	if err != nil {
		return nil, err
	}
	limits, err := probeFileLimits(ctx, seams, opts)
	if err != nil {
		return nil, err
	}
	perf, err := probePerf(ctx, seams, opts)
	if err != nil {
		return nil, err
	}
	results := append([]Result{device}, tools...)
	results = append(results, toolchain, subdir)
	results = append(results, limits...)
	results = append(results, perf...)
	return append(results, probeContrib(seams)...), nil
}

// Failed returns the names of the results whose status is fail, in
// probe order. The CLI turns a non-empty list into a runtime error
// so doctor exits nonzero on any failed probe, while warn and info
// results never affect the exit code.
func Failed(results []Result) []string {
	var names []string
	for _, result := range results {
		if result.Status == statusFail {
			names = append(names, result.Name)
		}
	}
	return names
}

func (s Seams) ready() error {
	if s.LookPath == nil || s.Stat == nil || s.Run == nil ||
		s.TruncateProbe == nil || s.Measure == nil {
		return errors.New("doctor seams are incomplete")
	}
	if s.Profile == profileDarwin && s.Hdiutil == nil {
		return errors.New("doctor seams are incomplete")
	}
	return nil
}

func probeDevice(ctx context.Context, seams Seams) (Result, error) {
	if err := contextErr(ctx, nil); err != nil {
		return Result{}, err
	}
	if err := seams.Stat(fuseDevicePath); err != nil {
		return fail(nameFuseDevice, err.Error()), nil
	}
	return pass(nameFuseDevice, fuseDevicePath), nil
}

func probeTools(ctx context.Context, seams Seams) ([]Result, error) {
	results := make([]Result, 0, len(toolSpecs))
	for _, spec := range toolSpecs {
		item, err := probeTool(ctx, seams, spec)
		if err != nil {
			return nil, err
		}
		results = append(results, item.result)
	}
	return results, nil
}

// probeImageToolchain aggregates Format.Tools. One complete stack is
// enough to avoid a failure. A missing stack is a warning. No
// complete stack fails the probe.
func probeImageToolchain(ctx context.Context, seams Seams) (Result, error) {
	if err := contextErr(ctx, nil); err != nil {
		return Result{}, err
	}
	stacks := container.Stacks(seams.LookPath)
	var ready []string
	for _, stack := range stacks {
		if stack.Complete() {
			ready = append(ready, stack.Format.Type())
		}
	}
	gaps := container.IncompleteDetail(stacks)
	missing := missingBinaries(stacks)
	switch {
	case len(ready) == 0:
		detail := "no complete image stack"
		if gaps != "" {
			detail += ": " + gaps
		}
		return fail(nameImageToolchain, withInstall(detail, missing)), nil
	case gaps == "":
		return pass(nameImageToolchain, strings.Join(ready, ", ")), nil
	default:
		detail := strings.Join(ready, ", ") + " ready; " + gaps
		return warn(nameImageToolchain, withInstall(detail, missing)), nil
	}
}

func missingBinaries(stacks []container.Stack) []string {
	var missing []string
	for _, stack := range stacks {
		missing = append(missing, stack.Missing...)
	}
	return missing
}

func probeTool(ctx context.Context, seams Seams, spec toolSpec) (parsedTool, error) {
	if err := contextErr(ctx, nil); err != nil {
		return parsedTool{}, err
	}
	path, err := seams.LookPath(spec.binary)
	if err != nil {
		detail := fmt.Sprintf("%s: %v", spec.binary, err)
		return parsedTool{result: fail(spec.name, withInstall(detail, []string{spec.binary}))}, nil
	}
	ver, detail, err := readVersion(ctx, seams, path, spec.hints)
	if err != nil {
		return parsedTool{}, err
	}
	if detail != "" {
		return parsedTool{result: fail(spec.name, detail)}, nil
	}
	return parsedTool{result: pass(spec.name, ver.raw), version: ver, ok: true}, nil
}

// probeSquashfuseSubdir checks the squashfuse subdir baseline. A
// missing binary warns, so an erofs-only host is not failed for a
// squashfs feature. An installed binary below 0.5.0 still fails.
func probeSquashfuseSubdir(ctx context.Context, seams Seams) (Result, error) {
	if err := contextErr(ctx, nil); err != nil {
		return Result{}, err
	}
	path, err := seams.LookPath("squashfuse_ll")
	if err != nil {
		return warn(nameSquashfuseSub, withInstall("squashfuse_ll is not installed", []string{"squashfuse_ll"})), nil
	}
	ver, detail, err := readVersion(ctx, seams, path, []string{"squashfuse_ll", "squashfuse"})
	if err != nil {
		return Result{}, err
	}
	if detail != "" {
		return fail(nameSquashfuseSub, detail), nil
	}
	if ver.below(0, 5, 0) {
		return fail(nameSquashfuseSub, ver.raw+" is below "+subdirBaselineText), nil
	}
	return pass(nameSquashfuseSub, ver.raw), nil
}

func pass(name, detail string) Result {
	return Result{Name: name, Status: statusPass, Detail: cleanDetail(detail)}
}

func fail(name, detail string) Result {
	return Result{Name: name, Status: statusFail, Detail: cleanDetail(detail)}
}

func warn(name, detail string) Result {
	return Result{Name: name, Status: statusWarn, Detail: cleanDetail(detail)}
}

func info(name, detail string) Result {
	return Result{Name: name, Status: statusInfo, Detail: cleanDetail(detail)}
}
