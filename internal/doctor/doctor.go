// Package doctor probes the local FUSE device and external tools.
// Command execution and stat go through seams so unit tests never
// touch host binaries. A failing probe is a diagnostic result. The
// only error is when the result set itself cannot be produced.
package doctor

import (
	"context"
	"errors"
	"fmt"
)

const (
	nameFuseDevice     = "fuse_device"
	nameSquashfuseLL   = "squashfuse_ll"
	nameFuseOverlayfs  = "fuse_overlayfs"
	nameFusermount3    = "fusermount3"
	nameMksquashfs     = "mksquashfs"
	nameGit            = "git"
	nameSquashfuseSub  = "squashfuse_subdir"
	statusPass         = "pass"
	statusFail         = "fail"
	statusWarn         = "warn"
	statusInfo         = "info"
	fuseDevicePath     = "/dev/fuse"
	subdirBaselineText = "0.5.0"
)

// Seams are the host operations a probe is allowed to perform.
// Tests replace them. Production fills them from the linux host
// package plus the stdlib truncate probe.
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

// toolSpecs is the external-tool probe list. squashfuse_subdir is
// not a binary; it reuses the squashfuse_ll version parsed here.
var toolSpecs = []toolSpec{
	{name: nameSquashfuseLL, binary: "squashfuse_ll", hints: []string{"squashfuse_ll", "squashfuse"}},
	{name: nameFuseOverlayfs, binary: "fuse-overlayfs", hints: []string{"fuse-overlayfs", "fuse_overlayfs"}},
	{name: nameFusermount3, binary: "fusermount3", hints: []string{"fusermount3", "fusermount"}},
	{name: nameMksquashfs, binary: "mksquashfs", hints: []string{"mksquashfs"}},
	{name: nameGit, binary: "git", hints: []string{"git"}},
}

// Probe runs every environment probe. A missing tool, an unparsable
// banner, or an unavailable FUSE device becomes a fail result; the
// storage probes degrade to warn or info instead. The options carry
// the configured storage directories and split settings. An error is
// returned only when the set cannot be built, including a cancelled
// context or seams that were not supplied.
func Probe(ctx context.Context, seams Seams, opts Options) ([]Result, error) {
	if err := contextErr(ctx, nil); err != nil {
		return nil, err
	}
	if err := seams.ready(); err != nil {
		return nil, err
	}
	device, err := probeDevice(ctx, seams)
	if err != nil {
		return nil, err
	}
	tools, squash, err := probeTools(ctx, seams)
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
	results = append(results, squashfuseSubdir(squash))
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

func probeTools(ctx context.Context, seams Seams) ([]Result, parsedTool, error) {
	results := make([]Result, 0, len(toolSpecs))
	var squash parsedTool
	for _, spec := range toolSpecs {
		item, err := probeTool(ctx, seams, spec)
		if err != nil {
			return nil, parsedTool{}, err
		}
		results = append(results, item.result)
		if spec.name == nameSquashfuseLL {
			squash = item
		}
	}
	return results, squash, nil
}

func probeTool(ctx context.Context, seams Seams, spec toolSpec) (parsedTool, error) {
	if err := contextErr(ctx, nil); err != nil {
		return parsedTool{}, err
	}
	path, err := seams.LookPath(spec.binary)
	if err != nil {
		return parsedTool{result: fail(spec.name, fmt.Sprintf("%s: %v", spec.binary, err))}, nil
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

func squashfuseSubdir(tool parsedTool) Result {
	if !tool.ok {
		return fail(nameSquashfuseSub, "squashfuse_ll: "+tool.result.Detail)
	}
	if tool.version.below(0, 5, 0) {
		return fail(nameSquashfuseSub, tool.version.raw+" is below "+subdirBaselineText)
	}
	return pass(nameSquashfuseSub, tool.version.raw)
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
