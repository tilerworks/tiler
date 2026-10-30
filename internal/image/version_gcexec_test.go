package image

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
)

func gcExecutionFixture(t *testing.T) (Repo, []ImageMeta, VersionGCPlan) {
	t.Helper()
	_, repo, nodes := removalFixture(t)
	for _, node := range nodes {
		seedGCActivity(t, repo, node, testActivityNow.Add(-60*24*time.Hour))
	}
	plan, err := PlanVersionGCLocked(repo, nil, testActivityNow.Add(-30*24*time.Hour), activityClock)
	if err != nil || len(plan.Candidates) != len(nodes) {
		t.Fatalf("fixture plan = %+v, %v", plan, err)
	}
	return repo, nodes, plan
}

func removedGCIDs(events []VersionGCOutcome) []string {
	var ids []string
	for _, event := range events {
		if event.Action == GCActionRemoved {
			ids = append(ids, event.ID)
		}
	}
	return ids
}

func TestVersionGCRevalidatesApprovalAndRetainedDependencies(t *testing.T) {
	for _, change := range []string{"unapproved child", "new child", "new reference", "metadata", "republished", "activity", "propagated ancestor"} {
		t.Run(change, func(t *testing.T) {
			repo, nodes, plan := gcExecutionFixture(t)
			pins := map[string]bool{}
			var want []string
			switch change {
			case "unapproved child":
				plan.Candidates = plan.Candidates[len(plan.Candidates)-1:]
			case "new child":
				child := incrementalNode(nodes[2], "unapproved child")
				seedVersions(t, repo, child)
				seedGCActivity(t, repo, child, testActivityNow.Add(-60*24*time.Hour))
			case "new reference":
				pins[nodes[2].ID] = true
			case "metadata", "republished":
				if change == "metadata" {
					nodes[0].Description = "changed after confirmation"
				} else {
					nodes[0].CreatedAt = nodes[0].CreatedAt.Add(time.Hour)
				}
				storeVersionIndexFixture(t, repo, removalManifest(repo, nodes))
				want = []string{nodes[2].ID, nodes[1].ID}
			case "activity":
				// Even an event still before the fixed cutoff invalidates approval.
				seedGCActivity(t, repo, nodes[0], testActivityNow.Add(-59*24*time.Hour))
				want = []string{nodes[2].ID, nodes[1].ID}
			case "propagated ancestor":
				sibling := incrementalNode(nodes[0], "active sibling")
				seedVersions(t, repo, sibling)
				if err := AppendVersionActivityLocked(repo, sibling.ID,
					activity.Event{Event: activity.EventMount, Phase: activity.PhaseSucceeded}, activityClock); err != nil {
					t.Fatal(err)
				}
				want = []string{nodes[2].ID, nodes[1].ID}
			}
			events, err := ExecuteVersionGCLocked(repo, plan, pins, activityClock)
			if err != nil || !slices.Equal(removedGCIDs(events), want) || len(events) != len(plan.Candidates) {
				t.Fatalf("execution = %+v, %v; want removals %v", events, err, want)
			}
			manifest, err := LoadVersionManifest(repo)
			if err != nil || manifest.Validate() != nil {
				t.Fatalf("retained dependencies broken: %+v, %v", manifest, err)
			}
		})
	}
}

func TestVersionGCDeletesFinalLeafButPreservesUnapprovedOrphans(t *testing.T) {
	for _, orphan := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete leaf", true: "unapproved orphan"}[orphan], func(t *testing.T) {
			repo, nodes, plan := gcExecutionFixture(t)
			orphanDir, _ := repo.ObjectDirPath(strings.Repeat("f", 64))
			if orphan {
				if err := os.MkdirAll(orphanDir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			events, err := ExecuteVersionGCLocked(repo, plan, nil, activityClock)
			if err != nil || len(removedGCIDs(events)) != len(nodes) {
				t.Fatalf("execution = %+v, %v", events, err)
			}
			if orphan {
				manifest, err := LoadVersionManifest(repo)
				if err != nil || len(manifest.Images) != 0 {
					t.Fatalf("empty metadata = %+v, %v", manifest, err)
				}
				if _, err := os.Stat(orphanDir); err != nil {
					t.Fatal("unapproved orphan was removed", err)
				}
				if got := readLeafFile(t, repo, fileSHA256Index); got != "" {
					t.Fatalf("removed versions remain indexed: %q", got)
				}
			} else if _, err := os.Stat(repo.Dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final leaf remains: %v", err)
			}
		})
	}
}

func TestVersionGCReportsAlreadyRemovedVersionsWithoutRecreation(t *testing.T) {
	repo, _, plan := gcExecutionFixture(t)
	if _, err := RemoveVersionRepositoryLocked(libraryRoot(repo), repo.LogsDir, repo.Name()); err != nil {
		t.Fatal(err)
	}
	events, err := ExecuteVersionGCLocked(repo, plan, nil, activityClock)
	if err != nil || len(events) != len(plan.Candidates) {
		t.Fatalf("gone execution = %+v, %v", events, err)
	}
	for _, event := range events {
		if event.Reason != "gone" {
			t.Fatalf("wrong disappearance result: %+v", event)
		}
	}
	if _, err := os.Stat(repo.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("execution recreated leaf: %v", err)
	}
}

func TestExactVersionRemovalRejectsUnapprovedChildrenAndRetainedMetadataChanges(t *testing.T) {
	repo, nodes, _ := gcExecutionFixture(t)
	before, _ := os.ReadFile(repo.MetaPath())
	if err := RemoveVersionSetLocked(repo, []string{nodes[0].ID}); err == nil {
		t.Fatal("parent removal expanded into unapproved children")
	}
	after, _ := os.ReadFile(repo.MetaPath())
	if !slices.Equal(before, after) {
		t.Fatal("rejected deletion changed metadata")
	}
	j := removalJournal{Name: repo.Name(), Before: removalManifest(repo, nodes),
		After: removalManifest(repo, slices.Clone(nodes[:1])), IDs: []string{nodes[2].ID, nodes[1].ID}}
	j.After.Images[0].Description = "corrupted retained metadata"
	if err := replayVersionRemoval(libraryRoot(repo), j, hooks{}); err == nil {
		t.Fatal("corrupted journal rewrote a retained version")
	}
}

func TestExactVersionRemovalPreservesExternalSourcesAndRefreshesIndexes(t *testing.T) {
	repo, nodes, plan := gcExecutionFixture(t)
	external := filepath.Join(t.TempDir(), "remote.sqsh")
	if err := os.WriteFile(external, []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	leaf, _ := repo.ObjectPayloadPath(nodes[2])
	if err := os.WriteFile(leaf, []byte(external+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureVersionIndexes(repo); err != nil {
		t.Fatal(err)
	}
	plan.Candidates = plan.Candidates[:1]
	events, err := ExecuteVersionGCLocked(repo, plan, nil, activityClock)
	if err != nil || !slices.Equal(removedGCIDs(events), []string{nodes[2].ID}) {
		t.Fatalf("linked removal = %+v, %v", events, err)
	}
	if data, err := os.ReadFile(external); err != nil || string(data) != "remote" {
		t.Fatalf("external source changed: %q, %v", data, err)
	}
	if strings.Contains(readLeafFile(t, repo, fileSHA256Index), nodes[2].ID) {
		t.Fatal("removed version remains indexed")
	}
}

func TestVersionGCDeletionFailureReportsPartialResultsAndRecovers(t *testing.T) {
	for _, stage := range []string{"metadata", "cleanup", "index"} {
		t.Run(stage, func(t *testing.T) {
			repo, nodes, plan := gcExecutionFixture(t)
			plan.Candidates = plan.Candidates[:2]
			original, _ := LoadVersionManifest(repo)
			seam := hooks{before: func(path, dst string) error {
				if (stage == "metadata" && dst == repo.MetaPath()) ||
					(stage == "cleanup" && dst == "" && filepath.Base(path) == nodes[1].ID) {
					return errors.New("injected deletion failure")
				}
				return nil
			}}
			if stage == "index" {
				if err := os.Mkdir(filepath.Join(repo.Dir, fileSHA256Index), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			events, err := executeVersionGC(repo, plan, nil, activityClock, seam)
			if err == nil {
				t.Fatal("injected failure succeeded")
			}
			want := 0
			switch stage {
			case "metadata":
				current, _ := LoadVersionManifest(repo)
				if !reflect.DeepEqual(current, original) {
					t.Fatal("failed metadata commit changed its source")
				}
			case "cleanup":
				want = 1
			case "index":
				want = 2
				if err := os.Remove(filepath.Join(repo.Dir, fileSHA256Index)); err != nil {
					t.Fatal(err)
				}
			}
			if len(removedGCIDs(events)) != want {
				t.Fatalf("incorrect actual results: %+v", events)
			}
			if err := recoverVersionRemoval(libraryRoot(repo), repo.Name(), hooks{}); err != nil {
				t.Fatal(err)
			}
			current, err := LoadVersionManifest(repo)
			if err != nil || len(current.Images) != 1 || current.Images[0].ID != nodes[0].ID {
				t.Fatalf("recovered metadata = %+v, %v", current, err)
			}
			if _, err := os.Stat(mustControl(t, libraryRoot(repo), repo.Name(), versionRemovalJournal)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("recovery journal remains: %v", err)
			}
		})
	}
}
