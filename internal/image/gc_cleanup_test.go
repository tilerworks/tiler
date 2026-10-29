package image

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func cleanupOrphan(t *testing.T, repo Repo, id string) string {
	t.Helper()
	dir, err := repo.ObjectDirPath(id)
	if err == nil {
		err = os.MkdirAll(dir, 0o755)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "payload"), []byte("orphan"), 0o644)
	}
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func pendingCleanup(t *testing.T, imagesDir string, repo Repo, nodes []ImageMeta, removeLeaf bool) removalJournal {
	t.Helper()
	j := removalJournal{Name: repo.Name(), Before: removalManifest(repo, nodes),
		After: removalManifest(repo, nodes[:1]), IDs: []string{nodes[2].ID, nodes[1].ID}}
	if removeLeaf {
		j.After.Images, j.IDs, j.Delete = nil, []string{nodes[2].ID, nodes[1].ID, nodes[0].ID}, true
	}
	if err := saveRemovalJournal(imagesDir, j, hooks{}); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestCleanupPreviewDoesNotRecoverOrRemoveObjects(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "orphans", true: "pending removal"}[pending], func(t *testing.T) {
			root, repo, nodes := removalFixture(t)
			cleanupOrphan(t, repo, strings.Repeat("f", 64))
			if pending {
				pendingCleanup(t, root, repo, nodes, false)
			}
			before, _ := cleanupTree(repo.Dir)
			body, _ := os.ReadFile(repo.MetaPath())
			plan, err := InspectCleanupLocked(root)
			if err != nil || plan.Pending != pending || len(plan.Items) != map[bool]int{false: 1, true: 2}[pending] {
				t.Fatalf("preview = %+v, %v", plan, err)
			}
			after, _ := cleanupTree(repo.Dir)
			current, _ := os.ReadFile(repo.MetaPath())
			if !sameCleanupTree(before, after) || !slices.Equal(body, current) {
				t.Fatal("preview performed cleanup")
			}
			for _, item := range plan.Items {
				if item.Kind != map[bool]string{false: "orphan", true: "pending-cleanup"}[pending] {
					t.Fatalf("unlabeled cleanup: %+v", item)
				}
			}
		})
	}
}

func TestCleanupDeletesOnlyUnchangedApprovedOrphans(t *testing.T) {
	for _, change := range []string{"none", "new orphan", "committed", "replaced", "modified"} {
		t.Run(change, func(t *testing.T) {
			root, repo, nodes := removalFixture(t)
			id := strings.Repeat("f", 64)
			dir := cleanupOrphan(t, repo, id)
			plan, err := InspectCleanupLocked(root)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "new orphan":
				cleanupOrphan(t, repo, strings.Repeat("e", 64))
			case "committed":
				node := nodes[0]
				node.ID, node.SHA256 = id, id
				storeVersionIndexFixture(t, repo, removalManifest(repo, append(nodes, node)))
			case "replaced":
				if err := os.Rename(dir, dir+"-old"); err != nil {
					t.Fatal(err)
				}
				cleanupOrphan(t, repo, id)
			case "modified":
				if err := os.WriteFile(filepath.Join(dir, "payload"), []byte("changed bytes"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			events, err := ExecuteCleanupLocked(root, plan)
			removed := change == "none" || change == "new orphan"
			if err != nil || len(events) != 1 || (events[0].Action == GCActionRemoved) != removed {
				t.Fatalf("execution = %+v, %v", events, err)
			}
			if change == "new orphan" {
				path, _ := repo.ObjectDirPath(strings.Repeat("e", 64))
				if _, err := os.Stat(path); err != nil {
					t.Fatal("unapproved orphan was removed", err)
				}
			}
		})
	}
}

func TestPendingCleanupRevalidatesTheJournalAndRepository(t *testing.T) {
	for _, change := range []string{"none", "journal", "replaced journal", "new orphan", "metadata"} {
		t.Run(change, func(t *testing.T) {
			root, repo, nodes := removalFixture(t)
			j := pendingCleanup(t, root, repo, nodes, true)
			plan, err := InspectCleanupLocked(root)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "journal":
				j.IDs[0], j.IDs[1] = j.IDs[1], j.IDs[0]
				if err := saveRemovalJournal(root, j, hooks{}); err != nil {
					t.Fatal(err)
				}
			case "replaced journal":
				if err := saveRemovalJournal(root, j, hooks{}); err != nil {
					t.Fatal(err)
				}
			case "new orphan":
				cleanupOrphan(t, repo, strings.Repeat("e", 64))
			case "metadata":
				nodes[0].Description = "changed"
				storeVersionIndexFixture(t, repo, removalManifest(repo, nodes))
			}
			events, err := ExecuteCleanupLocked(root, plan)
			if change == "metadata" {
				if err == nil {
					t.Fatal("conflicting metadata allowed recovery")
				}
				return
			}
			if err != nil || len(events) != len(plan.Items) {
				t.Fatalf("execution = %+v, %v", events, err)
			}
			for _, event := range events {
				if (event.Action == GCActionRemoved) != (change == "none") {
					t.Fatalf("unapproved recovery: %+v", event)
				}
			}
			if change == "none" {
				if _, err := os.Stat(repo.Dir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("leaf remains: %v", err)
				}
			}
		})
	}
}

func TestPendingCleanupFailureCanBePreviewedAndResumed(t *testing.T) {
	root, repo, nodes := removalFixture(t)
	pendingCleanup(t, root, repo, nodes, false)
	plan, err := InspectCleanupLocked(root)
	if err != nil {
		t.Fatal(err)
	}
	events, err := executeCleanupLocked(root, plan, hooks{before: func(path, dst string) error {
		if dst == "" && filepath.Base(path) == nodes[1].ID {
			return errors.New("interrupted cleanup")
		}
		return nil
	}})
	if !errors.Is(err, ErrCleanupPending) || len(removedGCIDs(events)) != 1 {
		t.Fatalf("partial recovery = %+v, %v", events, err)
	}
	plan, err = InspectCleanupLocked(root)
	if err != nil || !plan.Pending {
		t.Fatalf("resumed preview = %+v, %v", plan, err)
	}
	if _, err := ExecuteCleanupLocked(root, plan); err != nil {
		t.Fatal(err)
	}
	journal, err := controlFile(root, repo.Name(), versionRemovalJournal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains: %v", err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 1 {
		t.Fatalf("retained metadata = %+v, %v", manifest, err)
	}
}

func TestPendingCleanupRefusesUnsafeJournalAndNamespacePaths(t *testing.T) {
	for _, mode := range []string{"bad journal", "journal symlink", "namespace symlink", "object symlink", "unknown entry"} {
		t.Run(mode, func(t *testing.T) {
			root, repo, nodes := removalFixture(t)
			pendingCleanup(t, root, repo, nodes, true)
			path, err := controlFile(root, repo.Name(), versionRemovalJournal)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "bad journal":
				if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "journal symlink":
				external := filepath.Join(t.TempDir(), "journal")
				if err := os.Rename(path, external); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, path); err != nil {
					t.Skip(err)
				}
			case "namespace symlink":
				external := filepath.Join(t.TempDir(), "outside")
				if err := os.Rename(filepath.Dir(repo.Dir), external); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, filepath.Dir(repo.Dir)); err != nil {
					t.Skip(err)
				}
			case "object symlink":
				dir, _ := repo.ObjectDirPath(nodes[0].ID)
				external := filepath.Join(t.TempDir(), "outside")
				if err := os.Rename(dir, external); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, dir); err != nil {
					t.Skip(err)
				}
			case "unknown entry":
				if err := os.WriteFile(filepath.Join(repo.Dir, "unmanaged"), []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := InspectCleanupLocked(root); err == nil {
				t.Fatal("unsafe cleanup produced a plan")
			}
		})
	}
}

func TestPendingLeafCleanupListsExistingOrphansAndHandlesMissingMetadata(t *testing.T) {
	root, repo, nodes := removalFixture(t)
	pendingCleanup(t, root, repo, nodes, true)
	orphan := cleanupOrphan(t, repo, strings.Repeat("f", 64))
	if err := os.Remove(repo.MetaPath()); err != nil {
		t.Fatal(err)
	}
	plan, err := InspectCleanupLocked(root)
	if err != nil || len(plan.Items) != len(nodes)+1 || plan.Items[len(nodes)].Kind != "orphan" {
		t.Fatalf("missing-metadata cleanup plan = %+v, %v", plan, err)
	}
	if _, err := ExecuteCleanupLocked(root, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("approved orphan remains: %v", err)
	}
}

func TestOrphanCleanupDoesNotFollowExternalPayloadLinks(t *testing.T) {
	root, repo, _ := removalFixture(t)
	dir := cleanupOrphan(t, repo, strings.Repeat("f", 64))
	external := filepath.Join(t.TempDir(), "remote")
	if err := os.WriteFile(external, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(dir, "external")); err != nil {
		t.Skip(err)
	}
	plan, err := InspectCleanupLocked(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteCleanupLocked(root, plan); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(external); err != nil || string(data) != "keep" {
		t.Fatalf("external payload changed: %q, %v", data, err)
	}
}
