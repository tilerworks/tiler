package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
)

// withDummyConcatfs puts a do-nothing concatfs on PATH so the concat
// route passes the preflight tool lookup. The real binary is an
// optional contrib tool and is normally not installed; the other
// FUSE tools still come from the original PATH.
func withDummyConcatfs(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "concatfs")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// concatSource is the byte-block payload description used by the
// concat tests: a block directory plus the virtual file name
// concatfs exposes inside it.
func concatSource(dir string) *ConcatSource {
	return &ConcatSource{
		BlocksDir: filepath.Join(dir, "blocks"),
		ViewName:  "image.concat",
	}
}

func TestConcatReadOnlySingleMountsTransitFirst(t *testing.T) {
	withDummyConcatfs(t)
	req, dir := testRequest(t)
	req.Layers = nil
	req.ReadOnly = true
	req.Concat = concatSource(dir)
	k := newFakeKernel()
	w, err := mountTest(t, req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	concat := k.find("concatfs")
	if len(concat) != 1 {
		t.Fatalf("concatfs calls = %v", concat)
	}
	transit := concat[0][2]
	if concat[0][1] != req.Concat.BlocksDir {
		t.Errorf("concatfs source = %q, want the blocks dir", concat[0][1])
	}
	if transit != filepath.Join(w.runtimeDir, "concat") {
		t.Errorf("transit = %q, want it under the runtime dir", transit)
	}
	if !strings.HasPrefix(w.runtimeDir, filepath.Join(dir, "runtime", "up-")) {
		t.Errorf("runtime dir %s is not under runtime/up-<dev>-<ino>", w.runtimeDir)
	}
	if _, statErr := os.Stat(transit); statErr != nil {
		t.Errorf("transit point was not created: %v", statErr)
	}
	squash := k.find("squashfuse_ll")
	if len(squash) != 1 {
		t.Fatalf("squashfuse_ll calls = %v", squash)
	}
	// The base layer mounts the virtual file from the transit point
	// but still publishes the canonical image path as its identity.
	if squash[0][2] != "fsname="+req.Full {
		t.Errorf("identity = %q, want the canonical image path", squash[0][2])
	}
	if squash[0][3] != filepath.Join(transit, req.Concat.ViewName) {
		t.Errorf("bytes = %q, want the virtual concat file", squash[0][3])
	}
	if squash[0][4] != req.MergedDir {
		t.Errorf("point = %q", squash[0][4])
	}
	if len(k.find("fuse-overlayfs")) != 0 {
		t.Errorf("a direct concat mount ran fuse-overlayfs: %v", k.calls)
	}
	if w.UpperDir != "" {
		t.Errorf("read-only concat mount recorded upper %s", w.UpperDir)
	}
	// The transit point is the first recorded mount, so every
	// teardown reaches it last.
	if len(w.mounts) != 2 || w.mounts[0] != transit || w.mounts[1] != req.MergedDir {
		t.Fatalf("mounts = %v", w.mounts)
	}
	k.calls = nil
	if err := w.Unmount(); err != nil {
		t.Fatal(err)
	}
	unmounts := k.find("fusermount3")
	if len(unmounts) != 2 || unmounts[0][2] != req.MergedDir || unmounts[1][2] != transit {
		t.Fatalf("teardown = %v, want the merged view first and the transit last", unmounts)
	}
}

func TestConcatWritableOverlayKeepsTransitOutOfLowers(t *testing.T) {
	withDummyConcatfs(t)
	req, dir := testRequest(t)
	req.Concat = concatSource(dir)
	k := newFakeKernel()
	w, err := mountTest(t, req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	concat := k.find("concatfs")
	if len(concat) != 1 {
		t.Fatalf("concatfs calls = %v", concat)
	}
	transit := concat[0][2]
	squash := k.find("squashfuse_ll")
	if len(squash) != 3 {
		t.Fatalf("squashfuse_ll calls = %v", squash)
	}
	// Newest increment first, the base last; only the base member
	// reads from the concat view.
	if squash[2][2] != "fsname="+req.Full {
		t.Errorf("base identity = %q, want the canonical image path", squash[2][2])
	}
	if squash[2][3] != filepath.Join(transit, req.Concat.ViewName) {
		t.Errorf("base bytes = %q, want the virtual concat file", squash[2][3])
	}
	overlay := k.find("fuse-overlayfs")
	if len(overlay) != 1 {
		t.Fatalf("fuse-overlayfs calls = %v", overlay)
	}
	opts := overlay[0][2]
	lowerdir := strings.Join([]string{squash[0][4], squash[1][4], squash[2][4]}, ":")
	if !strings.Contains(opts, "lowerdir="+lowerdir) {
		t.Errorf("overlay opts %q missing the member lowers %q", opts, lowerdir)
	}
	if strings.Contains(opts, transit) {
		t.Errorf("the concat transit point leaked into the overlay: %q", opts)
	}
	if !strings.Contains(opts, "upperdir="+w.UpperDir) {
		t.Errorf("overlay opts %q missing the upper", opts)
	}
	// Teardown order: merged view, then the members, transit last.
	want := append([]string{transit}, squash[0][4], squash[1][4], squash[2][4], req.MergedDir)
	if !slices.Equal(w.mounts, want) {
		t.Fatalf("mounts = %v, want %v", w.mounts, want)
	}
}

func TestConcatReuseMatchesCanonicalIdentity(t *testing.T) {
	withDummyConcatfs(t)
	req, dir := testRequest(t)
	req.Layers = nil
	req.ReadOnly = true
	req.Concat = concatSource(dir)
	if err := os.MkdirAll(req.MergedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The live mount publishes the canonical image path, not the
	// transient concat view, so identity still matches and nothing
	// new is mounted.
	req.Table = []Entry{{Point: req.MergedDir, FSType: "fuse", Source: req.Full}}
	k := newFakeKernel()
	k.live[req.MergedDir] = true
	w, err := mountTest(t, req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if !w.Reused {
		t.Error("a matching live concat view must be reused")
	}
	if len(k.calls) != 0 {
		t.Errorf("reuse ran commands: %v", k.calls)
	}
	if len(w.mounts) != 0 {
		t.Errorf("reuse recorded mounts: %v", w.mounts)
	}
}

func TestConcatCleansStaleTransitBeforeRemount(t *testing.T) {
	withDummyConcatfs(t)
	req, dir := testRequest(t)
	req.Layers = nil
	req.ReadOnly = true
	req.Concat = concatSource(dir)
	if err := os.MkdirAll(req.MergedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Plan the runtime directory exactly like the engine will and
	// leave a live transit point there from an earlier interrupted
	// mount.
	dev, ino, err := devIno(req.MergedDir)
	if err != nil {
		t.Skipf("device and inode probing unavailable here: %v", err)
	}
	transit := filepath.Join(req.RuntimeDir, config.RuntimeDirName(dev, ino), "concat")
	if err := os.MkdirAll(transit, 0o755); err != nil {
		t.Fatal(err)
	}
	k := newFakeKernel()
	k.live[transit] = true
	w, err := mountTest(t, req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if w.Reused {
		t.Error("stale transit cleanup must not report reuse")
	}
	if k.calls[0][0] != "fusermount3" || k.calls[0][2] != transit {
		t.Fatalf("first call = %v, want the stale transit teardown", k.calls[0])
	}
	concat := k.find("concatfs")
	if len(concat) != 1 || concat[0][2] != transit {
		t.Fatalf("concatfs calls = %v, want one fresh mount of the same transit", concat)
	}
	if len(w.mounts) != 2 || w.mounts[0] != transit {
		t.Errorf("mounts = %v", w.mounts)
	}
}

func TestConcatFailureTearsDownTransit(t *testing.T) {
	withDummyConcatfs(t)
	req, dir := testRequest(t)
	req.Layers = nil
	req.ReadOnly = true
	req.Concat = concatSource(dir)
	rec := &recRunner{fail: "squashfuse_ll"}
	_, err := mountTest(t, req, rec.run, testHooks())
	if err == nil || !strings.Contains(err.Error(), "forced failure") {
		t.Fatalf("err = %v", err)
	}
	unmounts := rec.find("fusermount3")
	if len(unmounts) != 1 || filepath.Base(unmounts[0][2]) != "concat" {
		t.Fatalf("cleanup unmounts = %v, want the concat transit point", unmounts)
	}
	// The failed call also removes every directory it created.
	for _, created := range []string{req.MergedDir, req.RuntimeDir} {
		if _, statErr := os.Stat(created); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("created dir %s survived the failure: %v", created, statErr)
		}
	}
}

func TestConcatSourceValidation(t *testing.T) {
	withDummyConcatfs(t)
	tests := []struct {
		name   string
		concat *ConcatSource
	}{
		{name: "missing blocks dir", concat: &ConcatSource{ViewName: "image.concat"}},
		{name: "missing view name", concat: &ConcatSource{BlocksDir: "blocks"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := testRequest(t)
			req.Layers = nil
			req.ReadOnly = true
			req.Concat = tc.concat
			rec := &recRunner{}
			_, err := mountTest(t, req, rec.run, testHooks())
			if err == nil || !strings.Contains(err.Error(), "blocks dir and a view name") {
				t.Fatalf("err = %v, want a concat source refusal", err)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("tools ran despite the refusal: %v", rec.calls)
			}
		})
	}
}

func TestUnmountPathSweepsConcatTransit(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "view")
	runtime := filepath.Join(dir, "runtime")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	dev, ino, err := devIno(target)
	if err != nil {
		t.Skipf("device and inode probing unavailable here: %v", err)
	}
	transit := filepath.Join(runtime, config.RuntimeDirName(dev, ino), "concat")
	if err := os.MkdirAll(transit, 0o755); err != nil {
		t.Fatal(err)
	}
	k := newFakeKernel()
	k.live[target] = true
	k.live[transit] = true
	req := UnmountRequest{
		Dir: target,
		Table: []Entry{
			{Point: target, FSType: "fuse", Source: filepath.Join(dir, "image.sqsh")},
			{Point: transit, FSType: "fuse.concatfs", Source: filepath.Join(dir, "blocks")},
		},
		RuntimeDir: runtime,
	}
	removed, err := unmountPath(req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want the target and the concat transit", removed)
	}
	unmounts := k.find("fusermount3")
	if len(unmounts) != 2 || unmounts[0][2] != target || unmounts[1][2] != transit {
		t.Fatalf("calls = %v, want the target first and the transit swept after", unmounts)
	}
}
