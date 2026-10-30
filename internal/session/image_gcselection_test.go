//go:build linux || darwin

package session

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

func addGCRepository(t *testing.T, c ReferenceCoordinator, name string, nodes []image.ImageMeta) image.Repo {
	t.Helper()
	repo, err := image.OpenRepo(c.ImagesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	body, err := image.EncodeVersionManifest(image.VersionManifest{Version: image.VersionManifestVersion, Name: name, Images: nodes})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.MetaPath(), body, 0o644); err != nil {
		t.Fatal(err)
	}
	source, _ := image.OpenRepo(c.ImagesDir, "alice/app")
	for _, node := range nodes {
		src, _ := source.ObjectPayloadPath(node)
		dst, _ := repo.ObjectPayloadPath(node)
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func TestSelectedImagePlanningLeavesOtherActivityUntouched(t *testing.T) {
	c := newReferenceCoordinator(t)
	nodes := writeRemovalFixture(t, c)
	addGCRepository(t, c, "other/nested/app", nodes)
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	seedImageGCLogs(t, c, nodes, now.Add(-60*24*time.Hour))
	selector, err := image.NewNameSelector([]string{"alice/*", "alice/app", "missing/*"})
	if err != nil {
		t.Fatal(err)
	}
	plans, unmatched, err := c.PlanSelectedImages(now.Add(-30*24*time.Hour), nil, selector)
	if err != nil || len(plans) != 1 || plans[0].Name != "alice/app" || len(plans[0].Candidates) != 2 || !reflect.DeepEqual(unmatched, []string{"missing/*"}) {
		t.Fatalf("plans=%+v unmatched=%v err=%v", plans, unmatched, err)
	}
	for _, node := range nodes {
		path := filepath.Join(c.LogsDir, "images", "other", "nested", "app", node.ID+".log")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unselected log initialized: %v", err)
		}
	}
	selector, _ = image.NewNameSelector([]string{"missing/*"})
	empty, _, err := c.PlanSelectedImages(now, nil, selector)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty selection=%+v err=%v", empty, err)
	}
	added := addGCRepository(t, c, "alice/later", nodes)
	outcomes, err := c.ExecuteImages(plans, nil)
	if err != nil || len(outcomes) != 2 {
		t.Fatalf("outcomes=%+v err=%v", outcomes, err)
	}
	if _, err := os.Stat(added.MetaPath()); err != nil {
		t.Fatalf("new repository removed: %v", err)
	}
}

func TestSelectedImagePlanningPreservesSessionReferencesAndAncestors(t *testing.T) {
	c := newReferenceCoordinator(t)
	nodes := writeRemovalFixture(t, c)
	addGCRepository(t, c, "other/app", nodes)
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	seedImageGCLogs(t, c, nodes, now.Add(-60*24*time.Hour))
	rec := imageGCReference(t, nodes)
	rec.Actions[0].TipID, rec.Actions[0].ServedTip, rec.Actions[0].ServedID = nodes[1].ID, "", ""
	if err := c.Apply(nil, &rec); err != nil {
		t.Fatal(err)
	}
	selector, _ := image.NewNameSelector([]string{"alice/*"})
	plans, _, err := c.PlanSelectedImages(now.Add(-30*24*time.Hour), nil, selector)
	if err != nil || len(plans) != 1 || len(plans[0].Candidates) != 0 {
		t.Fatalf("protected plan=%+v err=%v", plans, err)
	}
}
