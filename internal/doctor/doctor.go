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
	fuseDevicePath     = "/dev/fuse"
	subdirBaselineText = "0.5.0"
)

// Seams are the host operations a probe is allowed to perform.
// Tests replace them. Production fills them from the linux host
// package.
type Seams struct {
	LookPath func(name string) (string, error)
	Stat     func(name string) error
	Run      func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Result is one probe line: a stable name, pass or fail, and a
// detail that keeps the reason for a failure.
type Result struct {
	Name   string
	Status string
	Detail string
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
// banner, or an unavailable FUSE device becomes a fail result. An
// error is returned only when the set cannot be built, including a
// cancelled context or seams that were not supplied.
func Probe(ctx context.Context, seams Seams) ([]Result, error) {
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
	return append(append([]Result{device}, tools...), squashfuseSubdir(squash)), nil
}

func (s Seams) ready() error {
	if s.LookPath == nil || s.Stat == nil || s.Run == nil {
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
