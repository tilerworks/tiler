package workspace

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMountAcceptsExplicitReadOnlyMember(t *testing.T) {
	req, _ := testRequest(t)
	req.Members = []MountMember{{Path: req.Full, FSName: "pinned", Format: "erofs"}}
	req.Full, req.Format, req.ReadOnly = "", "", true
	stubTools(t, "erofsfuse", "fusermount3")
	rec := &recRunner{}
	w, err := mountTest(t, req, rec.run, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Unmount() }()
	if calls := rec.find("erofsfuse"); len(calls) != 1 ||
		!slices.Contains(calls[0], "fsname=pinned") || len(rec.find("fuse-overlayfs")) != 0 {
		t.Fatalf("direct member mount: %v", rec.calls)
	}
}

func TestMixedMemberExecutorsOrderAndRollback(t *testing.T) {
	for _, fail := range []string{"", "squashfuse_ll"} {
		t.Run("failure="+fail, func(t *testing.T) {
			req, _ := testRequest(t)
			req.ReadOnly = true
			req.Members = []MountMember{
				{Path: req.Full, FSName: "base", Format: "squashfs"},
				{Path: req.Layers[0], FSName: "middle", Format: "erofs"},
				{Path: req.Layers[1], FSName: "tip", Format: "erofs"},
			}
			req = memberRequest(req)
			members, err := stack(req)
			if err != nil {
				t.Fatal(err)
			}
			rec := &recRunner{fail: fail}
			w := &Workspace{MergedDir: req.MergedDir, runtimeDir: req.RuntimeDir, run: rec.run}
			_, err = mountOverlay(req, members, w, testHooks())
			if (err != nil) != (fail != "") {
				t.Fatalf("mount: %v", err)
			}
			if len(rec.calls) < 3 || rec.calls[0][0] != "erofsfuse" ||
				rec.calls[1][0] != "erofsfuse" || rec.calls[2][0] != "squashfuse_ll" {
				t.Fatalf("wrong layer executors: %v", rec.calls)
			}
			if !slices.Equal(memberNames(members), []string{"tip", "middle", "base"}) {
				t.Fatalf("member order: %v", members)
			}
			lowers := plannedLowers(req.RuntimeDir, 3)
			if fail == "" {
				if calls := rec.find("fuse-overlayfs"); len(calls) != 1 ||
					!strings.Contains(calls[0][2], "lowerdir="+strings.Join(lowers, ":")) {
					t.Fatalf("merge: %v", calls)
				}
				if err := w.cleanup(); err != nil {
					t.Fatal(err)
				}
			} else if calls := rec.find("fusermount3"); len(calls) != 2 ||
				calls[0][2] != lowers[1] || calls[1][2] != lowers[0] {
				t.Fatalf("rollback: %v", calls)
			}
		})
	}
}

func TestMixedMemberPreflightAndPayloadValidation(t *testing.T) {
	req := Request{ReadOnly: true, Subdir: "src", Members: []MountMember{
		{Path: "base", Format: "erofs"}, {Path: "tip", Format: "squashfs"},
	}}
	tools, err := requiredTools(req)
	if err != nil || !slices.Equal(tools,
		[]string{"squashfuse_ll", "unsquashfs", "erofsfuse", "fusermount3", "fuse-overlayfs"}) {
		t.Fatalf("tools: %v, %v", tools, err)
	}
	bin := t.TempDir()
	for _, tool := range tools {
		if tool != "erofsfuse" {
			if err := os.WriteFile(filepath.Join(bin, tool), nil, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("PATH", bin)
	if err := lookTools(tools...); err == nil || !strings.Contains(err.Error(), "erofsfuse") {
		t.Fatalf("missing member tool: %v", err)
	}
	for _, format := range []string{"", "unknown", "dmg"} {
		req.Members[1].Format = format
		if _, err := requiredTools(req); err == nil {
			t.Fatalf("accepted member format %q", format)
		}
	}
	req.Members[1].Path = ""
	if _, err := stack(req); err == nil {
		t.Fatal("accepted empty member payload")
	}
	req.Concat = &ConcatSource{}
	if _, err := stack(req); err == nil {
		t.Fatal("accepted unmaterialized explicit members")
	}
}
