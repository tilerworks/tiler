package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
)

func TestUnmountPathNotMountedIsNoOp(t *testing.T) {
	dir := t.TempDir()
	runtime := filepath.Join(dir, "runtime")
	missing := filepath.Join(dir, "view")
	plain := filepath.Join(dir, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		target string
		table  []Entry
	}{
		{name: "absent target", target: missing},
		{name: "plain directory", target: plain},
		{
			name:   "snapshot the kernel already dropped",
			target: plain,
			table:  []Entry{{Point: plain, FSType: "fuse", Source: "/img/image.sqsh"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := newFakeKernel()
			removed, err := unmountPath(UnmountRequest{
				Dir: tc.target, Table: tc.table, RuntimeDir: runtime,
			}, k.run, k.seam())
			if err != nil {
				t.Fatal(err)
			}
			if removed != 0 {
				t.Errorf("removed = %d, want 0", removed)
			}
			if len(k.calls) != 0 {
				t.Errorf("an idempotent unmount ran commands: %v", k.calls)
			}
		})
	}
}

func TestUnmountPathRequiresFields(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		req  UnmountRequest
	}{
		{
			name: "missing dir",
			req:  UnmountRequest{RuntimeDir: filepath.Join(dir, "runtime")},
		},
		{
			name: "missing runtime dir",
			req:  UnmountRequest{Dir: filepath.Join(dir, "view")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := newFakeKernel()
			removed, err := unmountPath(tc.req, k.run, k.seam())
			if err == nil || !strings.Contains(err.Error(), "required") {
				t.Fatalf("err = %v, want a required-field refusal", err)
			}
			if removed != 0 {
				t.Errorf("removed = %d, want 0", removed)
			}
			if len(k.calls) != 0 {
				t.Errorf("tools ran despite the refusal: %v", k.calls)
			}
		})
	}
}

func TestUnmountPathTearsDownMergedThenLowersInReverse(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "view")
	runtime := filepath.Join(dir, "runtime")
	rt := filepath.Join(runtime, "up-9-9")
	lowers := []string{
		filepath.Join(rt, "lower-0"),
		filepath.Join(rt, "lower-1"),
		filepath.Join(rt, "lower-2"),
	}
	for _, lower := range lowers {
		if err := os.MkdirAll(lower, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	upper := filepath.Join(rt, "upper")
	if err := os.MkdirAll(upper, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(upper, "keep.txt"), []byte("prior work"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(rt, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	k := newFakeKernel()
	k.live[target] = true
	for _, lower := range lowers {
		k.live[lower] = true
	}
	req := UnmountRequest{
		Dir: target,
		Table: []Entry{{
			Point: target, FSType: "fuse.fuse-overlayfs", Source: "overlay",
			Upper: upper, Lowers: lowers,
		}},
		RuntimeDir: runtime,
	}
	removed, err := unmountPath(req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 4 {
		t.Errorf("removed = %d, want the merged view plus three lowers", removed)
	}
	unmounts := k.find("fusermount3")
	want := []string{target, lowers[2], lowers[1], lowers[0]}
	if len(unmounts) != len(want) {
		t.Fatalf("fusermount3 calls = %v", unmounts)
	}
	for i, call := range unmounts {
		if call[1] != "-u" || call[2] != want[i] {
			t.Errorf("unmount %d = %q, want -u %s", i, call, want[i])
		}
	}
	// Upper and work data survive for a remount of the same target.
	assertBytesFile(t, filepath.Join(upper, "keep.txt"), []byte("prior work"))
	if _, err := os.Stat(work); err != nil {
		t.Errorf("work dir was deleted: %v", err)
	}
}

func TestUnmountPathEscalatesToLazyUnmount(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "view")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	k := newFakeKernel()
	k.live[target] = true
	k.lazyOnly[target] = true
	req := UnmountRequest{
		Dir:        target,
		Table:      []Entry{{Point: target, FSType: "fuse", Source: "/img/image.sqsh"}},
		RuntimeDir: filepath.Join(dir, "runtime"),
	}
	removed, err := unmountPath(req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	unmounts := k.find("fusermount3")
	if len(unmounts) != 2 || unmounts[0][1] != "-u" || unmounts[1][1] != "-uz" {
		t.Fatalf("calls = %v, want -u then -uz", unmounts)
	}
}

func TestUnmountPathReportsResidue(t *testing.T) {
	dir := t.TempDir()
	// A regular file where the target's parent belongs keeps every
	// stat failing, simulating a mount that survives even the lazy
	// unmount; the wait must give up with a residue error.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(blocker, "merged")
	k := newFakeKernel()
	k.lazyOnly[target] = true
	req := UnmountRequest{
		Dir:        target,
		Table:      []Entry{{Point: target, FSType: "fuse", Source: "/img/image.sqsh"}},
		RuntimeDir: filepath.Join(dir, "runtime"),
	}
	removed, err := unmountPath(req, k.run, k.seam())
	if err == nil || !strings.Contains(err.Error(), "still mounted") {
		t.Fatalf("err = %v, want a residue error", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
	unmounts := k.find("fusermount3")
	if len(unmounts) != 2 || unmounts[0][1] != "-u" || unmounts[1][1] != "-uz" {
		t.Fatalf("calls = %v, want -u then -uz before giving up", unmounts)
	}
}

func TestUnmountPathSweepsVanishedOverlayLowers(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "view")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	dev, ino, err := devIno(target)
	if err != nil {
		t.Skipf("device and inode probing unavailable here: %v", err)
	}
	runtime := filepath.Join(dir, "runtime")
	rt := filepath.Join(runtime, config.RuntimeDirName(dev, ino))
	lowers := []string{
		filepath.Join(rt, "lower-0"),
		filepath.Join(rt, "lower-1"),
	}
	for _, lower := range lowers {
		if err := os.MkdirAll(lower, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	k := newFakeKernel()
	for _, lower := range lowers {
		k.live[lower] = true
	}
	// The overlay entry is already gone from the table; only the
	// reload still lists the orphaned squashfuse lowers.
	reload := func() ([]Entry, error) {
		return []Entry{
			{Point: lowers[0], FSType: "fuse", Source: "/img/incr.sqsh"},
			{Point: lowers[1], FSType: "fuse", Source: "/img/image.sqsh"},
		}, nil
	}
	req := UnmountRequest{
		Dir: target, RuntimeDir: runtime, Reload: reload,
	}
	removed, err := unmountPath(req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want both orphaned lowers", removed)
	}
	unmounts := k.find("fusermount3")
	if len(unmounts) != 2 || unmounts[0][2] != lowers[1] || unmounts[1][2] != lowers[0] {
		t.Fatalf("calls = %v, want reverse table order", unmounts)
	}
}

func TestUnmountPathTearsDownOwnedChildrenInReverse(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "forest")
	images := filepath.Join(dir, "images")
	src := filepath.Join(parent, "src")
	docs := filepath.Join(parent, "docs")
	tools := filepath.Join(parent, "tools")
	for _, point := range []string{src, docs, tools} {
		if err := os.MkdirAll(point, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The parent itself is not a listed mount; the combination lives
	// in its direct children, one of which is foreign.
	table := []Entry{
		{Point: src, FSType: "fuse", Source: filepath.Join(images, "alice/app/members/src.sqsh")},
		{Point: docs, FSType: "fuse", Source: filepath.Join(images, "alice/app/members/docs.sqsh")},
		{Point: tools, FSType: "fuse", Source: "/mnt/usb/tools"},
	}
	k := newFakeKernel()
	k.live[src] = true
	k.live[docs] = true
	k.live[tools] = true
	req := UnmountRequest{
		Dir:        parent,
		Table:      table,
		RuntimeDir: filepath.Join(dir, "runtime"),
		Owned: func(source string) bool {
			return strings.HasPrefix(source, images+string(filepath.Separator))
		},
	}
	removed, err := unmountPath(req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want the two library children", removed)
	}
	unmounts := k.find("fusermount3")
	if len(unmounts) != 2 || unmounts[0][2] != docs || unmounts[1][2] != src {
		t.Fatalf("calls = %v, want the owned children in reverse table order", unmounts)
	}
	if !k.live[tools] {
		t.Error("the foreign child mount was touched")
	}
	// A repeat is idempotent: plain directories remove nothing and
	// run nothing.
	removed, err = unmountPath(req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("repeat removed = %d, want 0", removed)
	}
	if len(k.find("fusermount3")) != 2 {
		t.Errorf("the repeat ran commands: %v", k.calls)
	}
}

func TestUnmountPathWithoutOwnedLeavesChildren(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "forest")
	child := filepath.Join(parent, "src")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	k := newFakeKernel()
	k.live[child] = true
	req := UnmountRequest{
		Dir: parent,
		Table: []Entry{{
			Point: child, FSType: "fuse",
			Source: "/images/alice/app/members/src.sqsh",
		}},
		RuntimeDir: filepath.Join(dir, "runtime"),
	}
	removed, err := unmountPath(req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 without an ownership predicate", removed)
	}
	if len(k.calls) != 0 {
		t.Errorf("the unlisted parent ran commands: %v", k.calls)
	}
	if !k.live[child] {
		t.Error("a child mount was torn down without an ownership predicate")
	}
}

func TestUnmountPathForeignMountGetsSinglePointOnly(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mnt")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	k := newFakeKernel()
	k.live[target] = true
	req := UnmountRequest{
		Dir: target,
		Table: []Entry{{
			Point: target, FSType: "ext4", Source: "/dev/sdz1",
			Lowers: []string{filepath.Join(dir, "elsewhere", "lower")},
		}},
		RuntimeDir: filepath.Join(dir, "runtime"),
	}
	removed, err := unmountPath(req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	unmounts := k.find("fusermount3")
	if len(unmounts) != 1 || unmounts[0][2] != target {
		t.Fatalf("calls = %v, want only the target point", unmounts)
	}
}

func TestUnmountPathReloadFailurePropagates(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "view")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := devIno(target); err != nil {
		t.Skipf("device and inode probing unavailable here: %v", err)
	}
	k := newFakeKernel()
	req := UnmountRequest{
		Dir: target, RuntimeDir: filepath.Join(dir, "runtime"),
		Reload: func() ([]Entry, error) {
			return nil, errors.New("mount table unreadable")
		},
	}
	if _, err := unmountPath(req, k.run, k.seam()); err == nil ||
		!strings.Contains(err.Error(), "mount table unreadable") {
		t.Fatalf("err = %v, want the reload failure", err)
	}
}
