//go:build linux || darwin

package session

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

func TestSelectedImageCleanupPreservesUnselectedOrphans(t *testing.T) {
	c := newReferenceCoordinator(t)
	nodes := writeRemovalFixture(t, c)
	other := addGCRepository(t, c, "other/app", nodes)
	repo, _ := image.OpenRepo(c.ImagesDir, "alice/app")
	var paths []string
	for _, r := range []image.Repo{repo, other} {
		path, _ := r.ObjectDirPath(strings.Repeat("f", 64))
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	selector, _ := image.NewNameSelector([]string{"alice/*"})
	plan, _, err := c.PlanSelectedImageGC(time.Now().Add(-24*time.Hour), nil, selector)
	if err != nil || len(plan.Cleanup.Items) != 1 || plan.Cleanup.Items[0].Name != repo.Name() {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("preview removed orphan", err)
		}
	}
	// Revalidation must not load an unselected repository's metadata.
	if err := os.WriteFile(other.MetaPath(), []byte("invalid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecuteImageGC(plan, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("selected orphan remains: %v", err)
	}
	if _, err := os.Stat(paths[1]); err != nil {
		t.Fatal("unselected orphan removed", err)
	}
}

func TestSelectedImageCleanupBlocksUnselectedPendingJournal(t *testing.T) {
	c := newReferenceCoordinator(t)
	nodes := writeRemovalFixture(t, c)
	addGCRepository(t, c, "other/app", nodes)
	journal := seedPendingImageCleanup(t, c, nodes)
	before, _ := os.ReadFile(journal)
	for _, pattern := range []string{"other/*", "missing/*", "alice/*"} {
		selector, _ := image.NewNameSelector([]string{pattern})
		plan, missing, err := c.PlanSelectedImageGC(time.Now(), nil, selector)
		switch pattern {
		case "other/*":
			if err == nil || !strings.Contains(err.Error(), "unselected image alice/app has pending cleanup") {
				t.Fatalf("unselected pending authorized: %+v %v", plan, err)
			}
		case "missing/*":
			if err != nil || len(missing) != 1 || len(plan.Cleanup.Items)+len(plan.Versions) != 0 {
				t.Fatalf("empty selection=%+v %v %v", plan, missing, err)
			}
		default:
			if err != nil || !plan.Cleanup.Pending || len(plan.Cleanup.Items) != 1 || len(plan.Versions) != 0 {
				t.Fatalf("selected pending=%+v %v", plan, err)
			}
		}
		if after, _ := os.ReadFile(journal); string(before) != string(after) {
			t.Fatal("preview changed pending journal")
		}
	}
}
