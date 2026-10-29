//go:build linux || darwin

package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/image"
)

func seedPendingImageCleanup(t *testing.T, c ReferenceCoordinator, nodes []image.ImageMeta) string {
	t.Helper()
	journal := struct {
		Name   string                `json:"name"`
		Before image.VersionManifest `json:"before"`
		After  image.VersionManifest `json:"after"`
		IDs    []string              `json:"ids"`
	}{Name: "alice/app", Before: image.VersionManifest{Version: image.VersionManifestVersion, Name: "alice/app", Images: nodes},
		After: image.VersionManifest{Version: image.VersionManifestVersion, Name: "alice/app", Images: nodes[:1]}, IDs: []string{nodes[1].ID}}
	body, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.ImagesDir, ".version-removal.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImageGCPendingCleanupPreviewIsStandaloneAndPreservesSessions(t *testing.T) {
	c := newReferenceCoordinator(t)
	nodes := writeRemovalFixture(t, c)
	journal := seedPendingImageCleanup(t, c, nodes)
	repo, _ := image.OpenRepo(c.ImagesDir, "alice/app")
	rec := removalRecord("kept", StateClosed, nodes[0].ID, "")
	rec.Actions[0].FullID = nodes[0].ID
	if err := c.Apply(nil, &rec); err != nil {
		t.Fatal(err)
	}
	recordPath, _ := c.recordPath(rec.Name)
	beforeRecord, _ := os.ReadFile(recordPath)
	beforeJournal, _ := os.ReadFile(journal)
	plan, err := c.PlanImageGC(time.Now().UTC(), nil)
	if err != nil || !plan.Cleanup.Pending || len(plan.Cleanup.Items) != 1 || len(plan.Versions) != 0 {
		t.Fatalf("cleanup plan = %+v, %v", plan, err)
	}
	for _, node := range nodes {
		dir, _ := repo.ObjectDirPath(node.ID)
		if _, err := os.Stat(filepath.Join(dir, activity.LogName)); !os.IsNotExist(err) {
			t.Fatalf("cleanup preview initialized unrelated activity: %v", err)
		}
	}
	if data, _ := os.ReadFile(journal); string(data) != string(beforeJournal) {
		t.Fatal("preview recovered removal")
	}
	events, err := c.ExecuteImageGC(plan, nil)
	if err != nil || len(events) != 1 || events[0].Action != image.GCActionRemoved {
		t.Fatalf("cleanup execution = %+v, %v", events, err)
	}
	if data, _ := os.ReadFile(recordPath); string(data) != string(beforeRecord) {
		t.Fatal("image cleanup changed the session")
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("confirmed journal remains: %v", err)
	}
	if _, err := c.PlanImageGC(time.Now().UTC(), nil); err != nil {
		t.Fatal("fresh expiration scan after cleanup failed", err)
	}
}

func TestImageGCCleanupRefusesSessionRecoveryAndReferencedPendingVersions(t *testing.T) {
	for _, mode := range []string{"session removal", "referenced pending"} {
		t.Run(mode, func(t *testing.T) {
			c := newReferenceCoordinator(t)
			nodes := writeRemovalFixture(t, c)
			rec := removalRecord("kept", StateClosed, nodes[1].ID, "")
			rec.Actions[0].FullID = nodes[0].ID
			if err := c.Apply(nil, &rec); err != nil {
				t.Fatal(err)
			}
			if mode == "session removal" {
				state := mustReferenceState(t, c.Store)
				if err := c.SaveReferenceJournal(ReferenceJournal{Version: ReferenceControlVersion,
					Generation: state.Generation + 1, Before: &rec}); err != nil {
					t.Fatal(err)
				}
			} else {
				seedPendingImageCleanup(t, c, nodes)
			}
			path, _ := c.recordPath(rec.Name)
			before, _ := os.ReadFile(path)
			if _, err := c.PlanImageGC(time.Now().UTC(), nil); err == nil {
				t.Fatal("unsafe state authorized cleanup")
			}
			if after, _ := os.ReadFile(path); string(before) != string(after) {
				t.Fatal("preview recovered or removed session")
			}
		})
	}
}

func TestImageGCCombinedPlanDeletesOnlyApprovedOrphansAndVersions(t *testing.T) {
	c := newReferenceCoordinator(t)
	nodes := writeRemovalFixture(t, c)
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	seedImageGCLogs(t, c, nodes, now.Add(-60*24*time.Hour))
	repo, _ := image.OpenRepo(c.ImagesDir, "alice/app")
	id := strings.Repeat("f", 64)
	dir, _ := repo.ObjectDirPath(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	plan, err := c.PlanImageGC(now.Add(-30*24*time.Hour), func() time.Time { return now })
	if err != nil || plan.Cleanup.Pending || len(plan.Cleanup.Items) != 1 || len(plan.Versions) != 1 {
		t.Fatalf("combined plan = %+v, %v", plan, err)
	}
	newID := strings.Repeat("e", 64)
	newDir, _ := repo.ObjectDirPath(newID)
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	events, err := c.ExecuteImageGC(plan, func() time.Time { return now })
	if err != nil || len(events) != len(nodes)+1 {
		t.Fatalf("combined execution = %+v, %v", events, err)
	}
	for _, event := range events {
		if event.Action != image.GCActionRemoved {
			t.Fatalf("approved deletion failed: %+v", event)
		}
	}
	if _, err := os.Stat(newDir); err != nil {
		t.Fatal("unapproved orphan was deleted by final leaf cleanup", err)
	}
}

func TestImageGCCleanupRechecksNewSessionReferencesBeforeRecovery(t *testing.T) {
	c := newReferenceCoordinator(t)
	nodes := writeRemovalFixture(t, c)
	seedPendingImageCleanup(t, c, nodes)
	plan, err := c.PlanImageGC(time.Now().UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := removalRecord("new-reference", StateClosed, nodes[1].ID, "")
	rec.Actions[0].FullID = nodes[0].ID
	if err := c.Apply(nil, &rec); err != nil {
		t.Fatal(err)
	}
	repo, _ := image.OpenRepo(c.ImagesDir, "alice/app")
	before, _ := os.ReadFile(repo.MetaPath())
	if events, err := c.ExecuteImageGC(plan, nil); err == nil || len(events) != 0 {
		t.Fatalf("new reference allowed pending cleanup: %+v, %v", events, err)
	}
	if after, _ := os.ReadFile(repo.MetaPath()); string(before) != string(after) {
		t.Fatal("refused cleanup changed metadata")
	}
}
