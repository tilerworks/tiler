package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// forestTest runs mountForest with an injected runner and the fake
// probe seams, skipping where this host cannot mount at all.
func forestTest(t *testing.T, req ForestRequest, run runFunc, seam hooks) (*Workspace, error) {
	t.Helper()
	w, err := mountForest(req, run, seam)
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("mounting unavailable here: %v", err)
	}
	return w, err
}

// forestRequest is a two-member forest over fresh temporary fixtures.
// The member files themselves never need to exist: the fake runner
// records the commands without reading them.
func forestRequest(t *testing.T) ForestRequest {
	t.Helper()
	dir := t.TempDir()
	return ForestRequest{
		ParentDir: filepath.Join(dir, "forest"),
		Partitions: []ForestMember{
			{Name: "src", Path: filepath.Join(dir, "members", "src.sqsh")},
			{Name: "docs", Path: filepath.Join(dir, "members", "docs.sqsh")},
		},
	}
}

// forestTable is the snapshot of a freshly mounted forest: one fuse
// entry per member point in manifest order, sourced by the member
// file path.
func forestTable(req ForestRequest) []Entry {
	table := make([]Entry, 0, len(req.Partitions))
	for _, m := range req.Partitions {
		table = append(table, Entry{
			Point:  filepath.Join(req.ParentDir, m.Name),
			FSType: "fuse",
			Source: m.Path,
		})
	}
	return table
}

// deadDaemonSeam models listed-but-dead points: the kernel-side
// mount is still attached, but the daemon behind it no longer
// answers, so every serving check fails while liveness follows the
// fake kernel.
func deadDaemonSeam(k *fakeKernel) hooks {
	return hooks{
		live:    k.isLive,
		serving: func(string) bool { return false },
		sleep:   func(time.Duration) {},
	}
}

func TestForestMountsMembersInManifestOrder(t *testing.T) {
	req := forestRequest(t)
	k := newFakeKernel()
	w, err := forestTest(t, req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	squash := k.find("squashfuse_ll")
	if len(squash) != len(req.Partitions) {
		t.Fatalf("squashfuse_ll calls = %v", squash)
	}
	for i, m := range req.Partitions {
		point := filepath.Join(req.ParentDir, m.Name)
		// The members form publishes the real member file path as
		// the mount table identity.
		if squash[i][2] != "fsname="+m.Path {
			t.Errorf("member %d identity = %q, want the member file path", i, squash[i][2])
		}
		if squash[i][3] != m.Path || squash[i][4] != point {
			t.Errorf("member %d mount = %q", i, squash[i])
		}
		if info, statErr := os.Stat(point); statErr != nil || !info.IsDir() {
			t.Errorf("member point %s was not created: %v", point, statErr)
		}
	}
	if len(k.find("fuse-overlayfs")) != 0 {
		t.Errorf("the forest ran fuse-overlayfs: %v", k.calls)
	}
	if w.Reused || w.UpperDir != "" {
		t.Errorf("reused = %v upper = %q, want a fresh read-only forest", w.Reused, w.UpperDir)
	}
	if w.MergedDir != req.ParentDir {
		t.Errorf("MergedDir = %q, want the parent directory", w.MergedDir)
	}
	if len(w.mounts) != 2 ||
		w.mounts[0] != filepath.Join(req.ParentDir, "src") ||
		w.mounts[1] != filepath.Join(req.ParentDir, "docs") {
		t.Fatalf("mounts = %v, want manifest order", w.mounts)
	}
	k.calls = nil
	if err := w.Unmount(); err != nil {
		t.Fatal(err)
	}
	unmounts := k.find("fusermount3")
	if len(unmounts) != 2 ||
		unmounts[0][2] != filepath.Join(req.ParentDir, "docs") ||
		unmounts[1][2] != filepath.Join(req.ParentDir, "src") {
		t.Fatalf("teardown = %v, want reverse manifest order", unmounts)
	}
}

func TestForestAppendsConfiguredOptions(t *testing.T) {
	req := forestRequest(t)
	req.SquashfsOptions = "allow_other"
	k := newFakeKernel()
	if _, err := forestTest(t, req, k.run, k.seam()); err != nil {
		t.Fatal(err)
	}
	squash := k.find("squashfuse_ll")
	if len(squash) != 2 {
		t.Fatalf("squashfuse_ll calls = %v", squash)
	}
	for i, m := range req.Partitions {
		want := "fsname=" + m.Path + ",allow_other"
		if squash[i][2] != want {
			t.Errorf("member %d opts = %q, want %q", i, squash[i][2], want)
		}
	}
}

func TestForestFSNameOverridesIdentity(t *testing.T) {
	req := forestRequest(t)
	req.Partitions[0].FSName = "/nas/images/alice/app/members/src.sqsh"
	k := newFakeKernel()
	if _, err := forestTest(t, req, k.run, k.seam()); err != nil {
		t.Fatal(err)
	}
	squash := k.find("squashfuse_ll")
	if len(squash) != 2 {
		t.Fatalf("squashfuse_ll calls = %v", squash)
	}
	if squash[0][2] != "fsname="+req.Partitions[0].FSName {
		t.Errorf("identity = %q, want the override", squash[0][2])
	}
	if squash[0][3] != req.Partitions[0].Path {
		t.Errorf("bytes = %q, want the member file path", squash[0][3])
	}
	if squash[1][2] != "fsname="+req.Partitions[1].Path {
		t.Errorf("member without an override identity = %q", squash[1][2])
	}
}

func TestForestReusesServingMembers(t *testing.T) {
	req := forestRequest(t)
	if err := os.MkdirAll(req.ParentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	req.Table = forestTable(req)
	k := newFakeKernel()
	for _, m := range req.Partitions {
		k.live[filepath.Join(req.ParentDir, m.Name)] = true
	}
	w, err := forestTest(t, req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if !w.Reused {
		t.Error("a healthy identical forest must be reused")
	}
	if len(k.calls) != 0 {
		t.Errorf("reuse ran commands: %v", k.calls)
	}
	if len(w.mounts) != 0 {
		t.Errorf("reuse recorded mounts: %v", w.mounts)
	}
}

func TestForestConflictOnDifferentIdentity(t *testing.T) {
	req := forestRequest(t)
	if err := os.MkdirAll(req.ParentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(req.ParentDir, "src")
	req.Table = []Entry{{Point: src, FSType: "fuse", Source: "/elsewhere/other.sqsh"}}
	k := newFakeKernel()
	k.live[src] = true
	_, err := forestTest(t, req, k.run, k.seam())
	if err == nil || !strings.Contains(err.Error(), "different view") {
		t.Fatalf("err = %v, want a conflict", err)
	}
	if len(k.calls) != 0 {
		t.Fatalf("a conflict ran commands: %v", k.calls)
	}
	// The rollback of a conflict on the first member leaves the
	// later member points uncreated.
	if _, statErr := os.Stat(filepath.Join(req.ParentDir, "docs")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the conflict created a later member point: %v", statErr)
	}
}

func TestForestDeadEntryOwnership(t *testing.T) {
	library := "/images"
	owned := func(source string) bool {
		return strings.HasPrefix(source, library+"/")
	}
	tests := []struct {
		name         string
		source       string
		owned        func(string) bool
		wantConflict bool
	}{
		{
			name:   "the own identity is cleaned and remounted",
			source: "", // filled with the member path
		},
		{
			name:         "a dead foreign entry is refused when ownership is known",
			source:       "/mnt/usb/backup.sqsh",
			owned:        owned,
			wantConflict: true,
		},
		{
			name:   "a dead library entry is cleaned and remounted",
			source: library + "/alice/app/members/old.sqsh",
			owned:  owned,
		},
		{
			name:   "a dead foreign entry is cleaned without a predicate",
			source: "/mnt/usb/backup.sqsh",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := forestRequest(t)
			if err := os.MkdirAll(req.ParentDir, 0o755); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(req.ParentDir, "src")
			if err := os.MkdirAll(src, 0o755); err != nil {
				t.Fatal(err)
			}
			source := tc.source
			if source == "" {
				source = req.Partitions[0].Path
			}
			req.Table = []Entry{{Point: src, FSType: "fuse", Source: source}}
			req.Owned = tc.owned
			k := newFakeKernel()
			k.live[src] = true
			_, err := forestTest(t, req, k.run, deadDaemonSeam(k))
			if tc.wantConflict {
				if err == nil || !strings.Contains(err.Error(), "different view") {
					t.Fatalf("err = %v, want a conflict", err)
				}
				if len(k.calls) != 0 {
					t.Fatalf("a refused foreign entry ran commands: %v", k.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			unmounts := k.find("fusermount3")
			if len(unmounts) != 1 || unmounts[0][1] != "-u" || unmounts[0][2] != src {
				t.Fatalf("cleanup calls = %v, want the dead member point", unmounts)
			}
			squash := k.find("squashfuse_ll")
			if len(squash) != 2 || squash[0][4] != src {
				t.Fatalf("squashfuse_ll calls = %v, want both members freshly mounted", squash)
			}
		})
	}
}

func TestForestRefusesNonEmptyMemberPoint(t *testing.T) {
	req := forestRequest(t)
	if err := os.MkdirAll(req.ParentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(req.ParentDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(src, "precious.txt")
	if err := os.WriteFile(precious, []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := newFakeKernel()
	_, err := forestTest(t, req, k.run, k.seam())
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err = %v, want a non-empty refusal", err)
	}
	if len(k.calls) != 0 {
		t.Fatalf("tools ran despite the refusal: %v", k.calls)
	}
	assertBytesFile(t, precious, []byte("user data"))
}

func TestForestRefusesUnlistedForeignMount(t *testing.T) {
	req := forestRequest(t)
	if err := os.MkdirAll(req.ParentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(req.ParentDir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	// The table does not list the point, but the live probe sees a
	// mount: something outside the combination occupies the point.
	k := newFakeKernel()
	k.live[src] = true
	_, err := forestTest(t, req, k.run, k.seam())
	if err == nil || !strings.Contains(err.Error(), "refusing to hide it") {
		t.Fatalf("err = %v, want a foreign mount refusal", err)
	}
	if len(k.calls) != 0 {
		t.Fatalf("tools ran despite the refusal: %v", k.calls)
	}
}

func TestForestValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ForestRequest)
	}{
		{name: "missing parent dir", mutate: func(r *ForestRequest) { r.ParentDir = "" }},
		{name: "no partitions", mutate: func(r *ForestRequest) { r.Partitions = nil }},
		{name: "empty member name", mutate: func(r *ForestRequest) { r.Partitions[0].Name = "" }},
		{name: "dot member name", mutate: func(r *ForestRequest) { r.Partitions[0].Name = "." }},
		{name: "climbing member name", mutate: func(r *ForestRequest) { r.Partitions[0].Name = "../up" }},
		{name: "member name with a separator", mutate: func(r *ForestRequest) { r.Partitions[0].Name = "a/b" }},
		{name: "missing member path", mutate: func(r *ForestRequest) { r.Partitions[0].Path = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := forestRequest(t)
			parent := req.ParentDir
			tc.mutate(&req)
			k := newFakeKernel()
			_, err := forestTest(t, req, k.run, k.seam())
			if err == nil {
				t.Fatal("want a refusal")
			}
			if len(k.calls) != 0 {
				t.Fatalf("tools ran despite the refusal: %v", k.calls)
			}
			if _, statErr := os.Stat(parent); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("the refusal created the parent directory: %v", statErr)
			}
		})
	}
}

func TestForestFailureRollsBackEarlierMembers(t *testing.T) {
	req := forestRequest(t)
	k := newFakeKernel()
	docs := filepath.Join(req.ParentDir, "docs")
	run := func(name string, args ...string) error {
		if name == "squashfuse_ll" && args[len(args)-1] == docs {
			k.calls = append(k.calls, append([]string{name}, args...))
			return errors.New("squashfuse_ll: forced failure")
		}
		return k.run(name, args...)
	}
	_, err := forestTest(t, req, run, k.seam())
	if err == nil || !strings.Contains(err.Error(), "forced failure") {
		t.Fatalf("err = %v", err)
	}
	// The already-mounted first member comes back down.
	unmounts := k.find("fusermount3")
	if len(unmounts) != 1 || unmounts[0][2] != filepath.Join(req.ParentDir, "src") {
		t.Fatalf("cleanup unmounts = %v, want the mounted member", unmounts)
	}
	// Every directory this call created is removed again.
	for _, dir := range []string{
		filepath.Join(req.ParentDir, "src"),
		filepath.Join(req.ParentDir, "docs"),
		req.ParentDir,
	} {
		if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("created dir %s survived the failure: %v", dir, statErr)
		}
	}
}

func TestForestAcceptsExistingParentContent(t *testing.T) {
	req := forestRequest(t)
	if err := os.MkdirAll(req.ParentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(req.ParentDir, "notes.txt")
	if err := os.WriteFile(kept, []byte("parent content"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := newFakeKernel()
	if _, err := forestTest(t, req, k.run, k.seam()); err != nil {
		t.Fatal(err)
	}
	// The forest never hides the parent itself, so its content stays.
	assertBytesFile(t, kept, []byte("parent content"))
	// The pre-existing parent is not this call's creation, so a
	// later rollback would keep it; here the mount simply succeeds.
	if len(k.find("squashfuse_ll")) != 2 {
		t.Fatalf("calls = %v", k.calls)
	}
}

func TestForestPreflightNeedsNoOverlayTools(t *testing.T) {
	if err := available(); err != nil {
		t.Skipf("mounting unavailable here: %v", err)
	}
	bin := t.TempDir()
	for _, tool := range []string{"squashfuse_ll", "fusermount3"} {
		script := filepath.Join(bin, tool)
		if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	if err := preflightForest(); err != nil {
		t.Fatalf("preflightForest = %v, want the forest to need only the two FUSE tools", err)
	}
}
