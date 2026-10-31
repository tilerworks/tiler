package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/durable"
)

func rebaseTestIdentity() RebaseIdentity {
	return RebaseIdentity{Instance: "incarnation", Image: "test/app", Runtime: "m1",
		ServedFull: "old-root", ServedTip: "old-tip", DesiredFull: "new-root", DesiredTip: "new-tip"}
}

func writeRebaseFile(t *testing.T, root, rel string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(rel), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPlanRebaseDeletionsUsesVisibleTrees(t *testing.T) {
	current, desired := t.TempDir(), t.TempDir()
	for _, rel := range []string{"unchanged", "recreated", "renamed", "tree/kept", "type-file", "type-link"} {
		writeRebaseFile(t, current, rel)
	}
	for _, rel := range []string{"unchanged", "recreated", "rename-source", "tree/kept", "tree/deleted",
		"deleted-tree/nested/child", "newly-published", "type-file/child", "type-link/child"} {
		writeRebaseFile(t, desired, rel)
	}
	if err := os.Remove(filepath.Join(current, "type-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(desired, filepath.Join(current, "type-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(desired, "deleted-link")); err != nil {
		t.Fatal(err)
	}
	got, err := planRebaseDeletions(current, desired)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"deleted-link", "deleted-tree", "newly-published", "rename-source", "tree/deleted"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deletions = %q, want %q", got, want)
	}
}

func TestPlanRebaseDeletionsHandlesEmptyCurrent(t *testing.T) {
	current, desired := t.TempDir(), t.TempDir()
	writeRebaseFile(t, desired, "published")
	got, err := planRebaseDeletions(current, desired)
	if err != nil || !reflect.DeepEqual(got, []string{"published"}) {
		t.Fatalf("deletions = %q, %v", got, err)
	}
}

func TestRebaseJournalBindsRecoveryAndAcknowledgement(t *testing.T) {
	runtime := t.TempDir()
	id := rebaseTestIdentity()
	plan := newRebasePlan(id, []string{"removed"})
	if err := saveRebasePlan(runtime, plan); err != nil {
		t.Fatal(err)
	}
	if ready, err := RebaseReady(runtime, id); err != nil || ready {
		t.Fatalf("unapplied readiness = %v, %v", ready, err)
	}
	for _, field := range []string{"instance", "runtime", "served", "desired", "image"} {
		other := id
		switch field {
		case "instance":
			other.Instance += "-reused"
		case "runtime":
			other.Runtime = "m2"
		case "served":
			other.ServedTip += "-other"
		case "desired":
			other.DesiredTip += "-other"
		case "image":
			other.Image += "-other"
		}
		if _, err := pendingRebasePlan(runtime, other); err == nil {
			t.Fatalf("accepted a different %s", field)
		}
	}
	if err := CompleteRebase(runtime, id); err == nil {
		t.Fatal("acknowledged an incomplete transition")
	}
	plan.Applied, plan.Ready = true, true
	if err := saveRebasePlan(runtime, plan); err != nil {
		t.Fatal(err)
	}
	if ready, err := RebaseReady(runtime, id); err != nil || !ready {
		t.Fatalf("completed readiness = %v, %v", ready, err)
	}
	id.ServedFull, id.ServedTip = "", ""
	if err := CompleteRebase(runtime, id); err != nil {
		t.Fatal(err)
	}
	if plan, err := loadRebasePlan(runtime); plan != nil || err != nil {
		t.Fatalf("journal survived acknowledgement: %+v, %v", plan, err)
	}
}

func TestRebaseJournalRejectsUnsafePlans(t *testing.T) {
	for _, deleted := range [][]string{{"."}, {"../upper"}, {"/absolute"}, {"a//b"}, {"a", "a"}, {"a", "a/b"}} {
		if err := saveRebasePlan(t.TempDir(), newRebasePlan(rebaseTestIdentity(), deleted)); err == nil {
			t.Fatalf("accepted deletions %q", deleted)
		}
	}
	for _, body := range []string{"{", "{}", `{"version":1,"unknown":true}`, "{}\n{}"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, rebaseJournal), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadRebasePlan(dir); err == nil {
			t.Fatalf("accepted invalid journal %s", body)
		}
	}
	dir := t.TempDir()
	if err := os.Symlink("elsewhere", filepath.Join(dir, rebaseJournal)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRebasePlan(dir); err == nil {
		t.Fatal("accepted a symlink journal")
	}
}

func TestRebaseJournalSyncFailurePreservesPreviousPlan(t *testing.T) {
	dir := t.TempDir()
	plan := newRebasePlan(rebaseTestIdentity(), []string{"removed"})
	if err := saveRebasePlan(dir, plan); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, rebaseJournal))
	if err != nil {
		t.Fatal(err)
	}
	original := durable.Sync
	t.Cleanup(func() { durable.Sync = original })
	durable.Sync = func(durable.File) error { return errors.New("injected sync failure") }
	plan.Applied = true
	if err := saveRebasePlan(dir, plan); err == nil || !strings.Contains(err.Error(), "sync failure") {
		t.Fatalf("save = %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, rebaseJournal))
	if err != nil || string(before) != string(after) {
		t.Fatalf("failed publication changed previous plan: %s, %v", after, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary journal residue = %v, %v", entries, err)
	}
}

func TestRebaseJournalRoundTripsLargeDeletionPlan(t *testing.T) {
	dir := t.TempDir()
	// Use legal component lengths and paths below Linux's path length limit.
	prefix := strings.Repeat(strings.Repeat("p", 200)+"/", 16)
	deleted := make([]string, 11000)
	for i := range deleted {
		deleted[i] = prefix + fmt.Sprintf("removed-%05d", i)
	}
	plan := newRebasePlan(rebaseTestIdentity(), deleted)
	if err := saveRebasePlan(dir, plan); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, rebaseJournal))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= 32<<20 {
		t.Fatalf("fixture size = %d, want more than the former 32 MiB limit", info.Size())
	}
	got, err := pendingRebasePlan(dir, plan.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, &plan) {
		t.Fatal("large deletion plan changed during recovery")
	}
}
