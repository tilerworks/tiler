//go:build linux

package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/durable"
)

func rebaseRequest(t *testing.T) Request {
	t.Helper()
	req, _ := testRequest(t)
	req.Layers = nil
	req.Rebase = &UpperRebase{Identity: rebaseTestIdentity(), Members: []MountMember{{Path: req.Full, Format: "squashfs"}}}
	return req
}

// Fake views expose fixture files directly; their contents disappear on detach.
func rebaseKernel(t *testing.T, req Request) *fakeKernel {
	t.Helper()
	k := newFakeKernel()
	k.repair = func(point string) {
		if underPrefix(point, rebaseTransit(req.RuntimeDir)) {
			entries, err := os.ReadDir(point)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if err := os.RemoveAll(filepath.Join(point, entry.Name())); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return k
}

func TestRebaseRecordsPlanBeforeApplyingAndHandlesEmptyUpper(t *testing.T) {
	req := rebaseRequest(t)
	k := rebaseKernel(t, req)
	applied := false
	run := func(name string, args ...string) error {
		point := args[len(args)-1]
		if name == "fuse-overlayfs" {
			switch filepath.Base(point) {
			case "desired":
				writeRebaseFile(t, point, "deleted")
			case "apply":
				plan, err := loadRebasePlan(req.RuntimeDir)
				if err != nil || plan == nil || plan.Applied || !reflect.DeepEqual(plan.Deleted, []string{"deleted"}) {
					t.Fatalf("plan before upper mutation = %+v, %v", plan, err)
				}
				writeRebaseFile(t, point, "deleted")
				applied = true
			case "merged":
				plan, err := loadRebasePlan(req.RuntimeDir)
				if err != nil || plan == nil || !plan.Applied || !plan.Ready || !applied {
					t.Fatalf("plan before mount = %+v, %v", plan, err)
				}
			}
		}
		if name == "fusermount3" && filepath.Base(point) == "apply" {
			if _, err := os.Lstat(filepath.Join(point, "deleted")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("deletion was not applied: %v", err)
			}
		}
		return k.run(name, args...)
	}
	w, err := mountTest(t, req, run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Unmount(); err != nil {
		t.Fatal(err)
	}
}

func TestRebaseRetriesOriginalPlanAfterPartialApply(t *testing.T) {
	req := rebaseRequest(t)
	if err := os.MkdirAll(req.RuntimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plan := newRebasePlan(req.Rebase.Identity, []string{"deleted"})
	if err := saveRebasePlan(req.RuntimeDir, plan); err != nil {
		t.Fatal(err)
	}
	k := rebaseKernel(t, req)
	run := func(name string, args ...string) error {
		point := args[len(args)-1]
		if name == "fuse-overlayfs" && strings.Contains(point, "/rebase/") {
			if filepath.Base(point) != "apply" {
				t.Fatalf("recomputed a saved deletion plan at %s", point)
			}
			// The deletion may already have generated its whiteout before a crash.
		}
		return k.run(name, args...)
	}
	w, err := mountTest(t, req, run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Unmount(); err != nil {
		t.Fatal(err)
	}
	got, err := loadRebasePlan(req.RuntimeDir)
	if err != nil || !got.Applied || !got.Ready || !reflect.DeepEqual(got.Deleted, plan.Deleted) {
		t.Fatalf("recovered plan = %+v, %v", got, err)
	}
}

func TestRebaseRetriesAfterPruneAndFinalMountFailure(t *testing.T) {
	req := rebaseRequest(t)
	req.PruneUpper = true
	upper := filepath.Join(req.RuntimeDir, "upper")
	writeRebaseFile(t, upper, "baked")
	writeRebaseFile(t, upper, "later")
	plan := newRebasePlan(req.Rebase.Identity, []string{"deleted"})
	plan.Applied = true
	if err := saveRebasePlan(req.RuntimeDir, plan); err != nil {
		t.Fatal(err)
	}
	k := rebaseKernel(t, req)
	fail := true
	run := func(name string, args ...string) error {
		point := args[len(args)-1]
		if name == "fuse-overlayfs" {
			if strings.Contains(point, "/rebase/") {
				t.Fatalf("recomputed a plan after upper pruning: %s", point)
			}
			if filepath.Base(point) == "lowercheck" {
				writeRebaseFile(t, point, "baked")
				// Give the prune comparator identical source metadata.
				info, err := os.Stat(filepath.Join(upper, "baked"))
				if err == nil {
					if err := os.Chtimes(filepath.Join(point, "baked"), info.ModTime(), info.ModTime()); err != nil {
						t.Fatal(err)
					}
				}
			}
			if point == req.MergedDir && fail {
				return errors.New("injected final mount failure")
			}
		}
		if name == "fusermount3" && filepath.Base(point) == "lowercheck" {
			if err := os.Remove(filepath.Join(point, "baked")); err != nil {
				return err
			}
		}
		return k.run(name, args...)
	}
	if _, err := mountTest(t, req, run, k.seam()); err == nil || !strings.Contains(err.Error(), "final mount failure") {
		t.Fatalf("mount = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(upper, "baked")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published file was not pruned: %v", err)
	}
	fail, req.PruneUpper = false, false
	w, err := mountTest(t, req, run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Unmount(); err != nil {
		t.Fatal(err)
	}
	got, err := loadRebasePlan(req.RuntimeDir)
	if err != nil || !got.Ready || !reflect.DeepEqual(got.Deleted, []string{"deleted"}) {
		t.Fatalf("retry treated pruned files as user deletions: %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(upper, "later")); err != nil {
		t.Fatalf("lost later upper edit: %v", err)
	}
}

func TestRebaseJournalFailureAbortsBeforePruneOrFinalMount(t *testing.T) {
	req := rebaseRequest(t)
	req.PruneUpper = true
	upper := filepath.Join(req.RuntimeDir, "upper")
	writeRebaseFile(t, upper, "baked")
	k := rebaseKernel(t, req)
	original := durable.Sync
	t.Cleanup(func() { durable.Sync = original })
	durable.Sync = func(durable.File) error { return errors.New("injected journal failure") }
	run := func(name string, args ...string) error {
		point := args[len(args)-1]
		if name == "fuse-overlayfs" && (filepath.Base(point) == "apply" || filepath.Base(point) == "lowercheck" || point == req.MergedDir) {
			t.Fatalf("mutated the upper or mounted despite journal failure: %s", point)
		}
		return k.run(name, args...)
	}
	if _, err := mountTest(t, req, run, k.seam()); err == nil || !strings.Contains(err.Error(), "journal failure") {
		t.Fatalf("mount = %v", err)
	}
	if _, err := os.Stat(filepath.Join(upper, "baked")); err != nil {
		t.Fatalf("lost upper data after journal failure: %v", err)
	}
}

func TestApplyRebaseDeletionsRejectsSymlinkParents(t *testing.T) {
	view := t.TempDir()
	writeRebaseFile(t, view, "keep/file")
	if err := os.Symlink("keep", filepath.Join(view, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := applyRebaseDeletions(view, []string{"alias/file"}); err == nil {
		t.Fatal("followed a replacement symlink while deleting")
	}
	if _, err := os.Stat(filepath.Join(view, "keep/file")); err != nil {
		t.Fatalf("deleted the symlink target: %v", err)
	}
}

func TestRebaseCleansInterruptedViewsBeforeTheirLowers(t *testing.T) {
	req := rebaseRequest(t)
	k := rebaseKernel(t, req)
	root := rebaseTransit(req.RuntimeDir)
	current, lower := filepath.Join(root, "current"), filepath.Join(root, "lower-0")
	// An interrupted process can leave table rows in an unexpected order.
	for _, point := range []string{current, lower} {
		if err := os.MkdirAll(point, 0o755); err != nil {
			t.Fatal(err)
		}
		req.Table = append(req.Table, Entry{Point: point, FSType: "fuse"})
		k.live[point] = true
	}
	plan := newRebasePlan(req.Rebase.Identity, nil)
	plan.Applied = true
	if err := saveRebasePlan(req.RuntimeDir, plan); err != nil {
		t.Fatal(err)
	}
	run := func(name string, args ...string) error {
		if name == "fusermount3" && args[len(args)-1] == lower && k.live[current] {
			t.Fatal("detached a lower still backing an interrupted comparison view")
		}
		return k.run(name, args...)
	}
	w, err := mountTest(t, req, run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if k.live[current] || k.live[lower] {
		t.Fatal("left interrupted comparison mounts attached")
	}
	if err := w.Unmount(); err != nil {
		t.Fatal(err)
	}
}

func TestRebaseSyncFailureKeepsThePlanUnapplied(t *testing.T) {
	req := rebaseRequest(t)
	upper := filepath.Join(req.RuntimeDir, "upper")
	writeRebaseFile(t, upper, "later")
	plan := newRebasePlan(req.Rebase.Identity, []string{"deleted"})
	if err := saveRebasePlan(req.RuntimeDir, plan); err != nil {
		t.Fatal(err)
	}
	k := rebaseKernel(t, req)
	original := durable.Sync
	t.Cleanup(func() { durable.Sync = original })
	durable.Sync = func(file durable.File) error {
		if f, ok := file.(*os.File); ok && underPrefix(f.Name(), upper) {
			return errors.New("injected upper sync failure")
		}
		return original(file)
	}
	if _, err := mountTest(t, req, k.run, k.seam()); err == nil || !strings.Contains(err.Error(), "upper sync failure") {
		t.Fatalf("mount = %v", err)
	}
	got, err := loadRebasePlan(req.RuntimeDir)
	if err != nil || got.Applied || got.Ready || !reflect.DeepEqual(got.Deleted, plan.Deleted) {
		t.Fatalf("advanced the journal before whiteout durability: %+v, %v", got, err)
	}
	for _, call := range k.find("fuse-overlayfs") {
		if call[len(call)-1] == req.MergedDir {
			t.Fatal("mounted a view with undurable whiteouts")
		}
	}
}

func TestRebaseSyncOnlyVisitsDeletionMarkersAndParents(t *testing.T) {
	upper := t.TempDir()
	writeRebaseFile(t, upper, "nested/deleted")
	writeRebaseFile(t, upper, "private")
	if err := os.Chmod(filepath.Join(upper, "private"), 0o200); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(upper, "unrelated"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(upper, "unrelated"), 0o700) })
	if err := os.Chmod(filepath.Join(upper, "unrelated"), 0); err != nil {
		t.Fatal(err)
	}
	original := durable.Sync
	t.Cleanup(func() { durable.Sync = original })
	var synced []string
	durable.Sync = func(file durable.File) error {
		f := file.(*os.File)
		rel, err := filepath.Rel(upper, f.Name())
		if err != nil {
			return err
		}
		synced = append(synced, rel)
		return original(file)
	}
	if err := syncRebaseUpper(upper, []string{"nested/deleted", "nested/hidden/removed"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"nested/deleted", "nested", "."}; !reflect.DeepEqual(synced, want) {
		t.Fatalf("synced %q, want only deletion metadata %q", synced, want)
	}
	info, err := os.Stat(filepath.Join(upper, "private"))
	if err != nil || info.Mode().Perm() != 0o200 {
		t.Fatalf("changed unrelated file permissions: %v, %v", info, err)
	}
}
