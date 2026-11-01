package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBlockMembersUseSeparateViewsAndRollbackInDependencyOrder(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "container failure"}[fail], func(t *testing.T) {
			withDummyConcatfs(t)
			req, dir := testRequest(t)
			req.Members = []MountMember{
				{Path: filepath.Join(dir, "base", "image.sqsh-concat-view"), FSName: "base", Format: "squashfs", Blocks: []string{filepath.Join(dir, "base block")}},
				{Path: filepath.Join(dir, "tip", "image.sqsh-concat-view"), FSName: "tip", Format: "squashfs", Blocks: []string{filepath.Join(dir, "tip block")}},
			}
			k := newFakeKernel()
			run := func(name string, args ...string) error {
				if name == "squashfuse_ll" && fail {
					return errors.New("injected container failure")
				}
				return k.run(name, args...)
			}
			w, err := mountTest(t, req, run, k.seam())
			if fail {
				if err == nil || !strings.Contains(err.Error(), "container failure") {
					t.Fatalf("mount: %v", err)
				}
				unmounts := k.find("fusermount3")
				if len(unmounts) != 2 || filepath.Base(unmounts[0][2]) != "concat-1" || filepath.Base(unmounts[1][2]) != "concat-0" {
					t.Fatalf("rollback: %v", unmounts)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls := k.find("concatfs"); len(calls) != 2 || !slices.Equal(calls[0][3:], []string{"-o", "ro"}) {
				t.Fatalf("concat mounts: %v", calls)
			}
			calls := k.find("squashfuse_ll")
			for i, identity := range []string{"tip", "base"} {
				point := filepath.Join(req.RuntimeDir, "concat-"+string(rune('0'+i)), "image.sqsh-concat-view")
				if !slices.Contains(calls[i], "fsname="+identity) || !slices.Contains(calls[i], point) {
					t.Fatalf("container mount: %v", calls[i])
				}
			}
			data, err := os.ReadFile(filepath.Join(req.RuntimeDir, "block-data-0", "image.sqsh-concat-view"))
			if err != nil || string(data) != req.Members[1].Blocks[0]+"\n" {
				t.Fatalf("resolved descriptor: %q, %v", data, err)
			}
			if err := w.cleanup(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSingleFileRecoveryCleansAnOldConcatTransit(t *testing.T) {
	req, _ := testRequest(t)
	req.Layers, req.ReadOnly = nil, true
	point := filepath.Join(req.RuntimeDir, "concat-0")
	if err := os.MkdirAll(point, 0o755); err != nil {
		t.Fatal(err)
	}
	req.Table = []Entry{{Point: point, FSType: "fuse.concatfs"}}
	k := newFakeKernel()
	k.live[point] = true
	w, err := mountTest(t, req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if k.live[point] {
		t.Fatal("left an old concat mount attached after switching to reassembly")
	}
	if err := w.Unmount(); err != nil {
		t.Fatal(err)
	}
}
