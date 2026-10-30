//go:build linux || darwin

package session

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/image"
)

func imageGCLogDir(c ReferenceCoordinator) string {
	return filepath.Join(c.LogsDir, "images", "alice", "app")
}

func seedImageGCLogs(t *testing.T, c ReferenceCoordinator, nodes []image.ImageMeta, at time.Time) {
	t.Helper()
	dir := imageGCLogDir(c)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		w, err := activity.OpenLog(c.LogsDir, dir, node.ID+".log", func() time.Time { return at })
		if err == nil {
			_, err = w.Append(activity.Event{Event: activity.EventMount, Phase: activity.PhaseSucceeded})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func imageGCReference(t *testing.T, nodes []image.ImageMeta) Record {
	t.Helper()
	rec := removalRecord("retained", StateClosed, nodes[1].ID, nodes[0].ID)
	rec.Actions[0].FullID, rec.Actions[0].ServedID = nodes[0].ID, nodes[0].ID
	rec.Actions[0].Status = StatusPending
	return rec
}

func TestImageGCPlansFromAuthoritativeDesiredAndServedReferences(t *testing.T) {
	c := newReferenceCoordinator(t)
	nodes := writeRemovalFixture(t, c)
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	seedImageGCLogs(t, c, nodes, now.Add(-60*24*time.Hour))
	rec := imageGCReference(t, nodes)
	rec.Actions[0].TipID, rec.Actions[0].ServedTip = nodes[0].ID, nodes[1].ID
	if err := c.Apply(nil, &rec); err != nil {
		t.Fatal(err)
	}
	state := mustReferenceState(t, c.Store)
	for _, target := range []ReferenceTarget{{"alice/app", nodes[0].ID}, {"alice/app", nodes[1].ID}} {
		path, _ := c.referenceBucketPath(target.Name, target.Tip)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	plans, err := c.PlanImages(now.Add(-30*24*time.Hour), nil)
	if err != nil || len(plans) != 1 || len(plans[0].Candidates) != 0 {
		t.Fatalf("reference protection = %+v, %v", plans, err)
	}
	if after := mustReferenceState(t, c.Store); !reflect.DeepEqual(state, after) {
		t.Fatal("read-only reference scan rewrote the inventory")
	}
	for _, target := range []ReferenceTarget{{"alice/app", nodes[0].ID}, {"alice/app", nodes[1].ID}} {
		path, _ := c.referenceBucketPath(target.Name, target.Tip)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preview rebuilt reference shard: %v", err)
		}
	}
}

func TestImageGCRefusesUnsafeReferenceAndRecoveryStatesWithoutMutation(t *testing.T) {
	for _, mode := range []string{"session deletion", "bad journal", "dirty state", "bad record", "missing source", "unindexed missing source", "image removal"} {
		t.Run(mode, func(t *testing.T) {
			c := newReferenceCoordinator(t)
			nodes := writeRemovalFixture(t, c)
			rec := imageGCReference(t, nodes)
			if err := c.Apply(nil, &rec); err != nil {
				t.Fatal(err)
			}
			recordPath, _ := c.recordPath(rec.Name)
			switch mode {
			case "session deletion":
				if err := c.SaveReferenceJournal(ReferenceJournal{
					Version: ReferenceControlVersion, Generation: 2, Before: &rec,
				}); err != nil {
					t.Fatal(err)
				}
			case "bad journal":
				path, _ := c.referencePath("journal.json")
				if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "dirty state":
				state := mustReferenceState(t, c.Store)
				state.Dirty = true
				if err := c.SaveReferenceState(state); err != nil {
					t.Fatal(err)
				}
			case "bad record":
				if err := os.WriteFile(recordPath, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing source", "unindexed missing source":
				if err := os.Remove(recordPath); err != nil {
					t.Fatal(err)
				}
				if mode == "unindexed missing source" {
					if err := c.SaveReferenceState(ReferenceState{Version: ReferenceControlVersion}); err != nil {
						t.Fatal(err)
					}
				}
			case "image removal":
				if err := os.WriteFile(filepath.Join(c.ImagesDir, ".version-removal.json"), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, beforeErr := os.ReadFile(recordPath)
			plans, err := c.PlanImages(time.Now().UTC(), nil)
			if err == nil || len(plans) != 0 {
				t.Fatalf("unsafe %s authorized a plan: %+v, %v", mode, plans, err)
			}
			after, afterErr := os.ReadFile(recordPath)
			if string(before) != string(after) || os.IsNotExist(beforeErr) != os.IsNotExist(afterErr) {
				t.Fatal("preview changed the authoritative session source")
			}
			for _, node := range nodes {
				path := filepath.Join(imageGCLogDir(c), node.ID+".log")
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("failed scan changed image activity: %v", err)
				}
			}
		})
	}
}

func TestImageGCRejectsReferencesToMissingVersions(t *testing.T) {
	c := newReferenceCoordinator(t)
	nodes := writeRemovalFixture(t, c)
	rec := imageGCReference(t, nodes)
	rec.Actions[0].TipID = strings.Repeat("9", 64)
	if err := c.Apply(nil, &rec); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PlanImages(time.Now(), nil); err == nil {
		t.Fatal("missing referenced version did not block expiry planning")
	}
}

func TestImageGCRejectsInconsistentRecordedRootsBeforeInitialization(t *testing.T) {
	for _, mode := range []string{"desired mismatch", "desired missing", "served mismatch", "served missing"} {
		for _, seeded := range []bool{false, true} {
			name := mode + "/missing activity"
			if seeded {
				name = mode + "/old activity"
			}
			t.Run(name, func(t *testing.T) {
				c := newReferenceCoordinator(t)
				nodes := writeRemovalFixture(t, c)
				repo, _ := image.OpenRepo(c.ImagesDir, "alice/app")
				manifest, err := image.LoadVersionManifest(repo)
				if err != nil {
					t.Fatal(err)
				}
				second := nodes[0]
				second.ID, second.SHA256 = strings.Repeat("9", 64), strings.Repeat("9", 64)
				nodes = append(nodes, second)
				manifest.Images = nodes
				body, err := image.EncodeVersionManifest(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(repo.MetaPath(), body, 0o644); err != nil {
					t.Fatal(err)
				}
				dir, _ := repo.ObjectDirPath(second.ID)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
				if seeded {
					seedImageGCLogs(t, c, nodes, now.Add(-60*24*time.Hour))
				}
				rec := imageGCReference(t, nodes)
				root, tip := nodes[0].ID, second.ID
				if strings.Contains(mode, "missing") {
					root, tip = strings.Repeat("8", 64), nodes[1].ID
				}
				if strings.HasPrefix(mode, "desired") {
					rec.Actions[0].FullID, rec.Actions[0].TipID = root, tip
				} else {
					rec.Actions[0].ServedID, rec.Actions[0].ServedTip = root, tip
				}
				if err := c.Apply(nil, &rec); err != nil {
					t.Fatal(err)
				}
				recordPath, _ := c.recordPath(rec.Name)
				before := make(map[string]string)
				for _, path := range []string{repo.MetaPath(), recordPath} {
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					before[path] = string(data)
				}
				for _, node := range nodes {
					path := filepath.Join(imageGCLogDir(c), node.ID+".log")
					data, err := os.ReadFile(path)
					if seeded && err != nil {
						t.Fatal(err)
					}
					before[path] = string(data)
				}
				plans, err := c.PlanImages(now.Add(-30*24*time.Hour), func() time.Time { return now })
				if err == nil || !strings.Contains(err.Error(), "recorded root") || len(plans) != 0 {
					t.Fatalf("inconsistent root authorized a plan: %+v, %v", plans, err)
				}
				for path, want := range before {
					data, err := os.ReadFile(path)
					missingLog := !seeded && strings.HasSuffix(path, ".log")
					if missingLog {
						if !os.IsNotExist(err) {
							t.Fatalf("failed scan initialized %s: %v", path, err)
						}
					} else if err != nil || string(data) != want {
						t.Fatalf("failed scan changed %s: %v", path, err)
					}
				}
			})
		}
	}
}
