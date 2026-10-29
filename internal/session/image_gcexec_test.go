//go:build linux || darwin

package session

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/image"
)

func imageGCExecutionFixture(t *testing.T) (ReferenceCoordinator, []image.ImageMeta, []image.VersionGCPlan, activity.Clock) {
	t.Helper()
	c := newReferenceCoordinator(t)
	nodes := writeRemovalFixture(t, c)
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	seedImageGCLogs(t, c, nodes, now.Add(-60*24*time.Hour))
	plans, err := c.PlanImages(now.Add(-30*24*time.Hour), clock)
	if err != nil || len(plans) != 1 || len(plans[0].Candidates) != len(nodes) {
		t.Fatalf("fixture plan = %+v, %v", plans, err)
	}
	return c, nodes, plans, clock
}

func TestExecuteImageGCRechecksAuthoritativeReferences(t *testing.T) {
	c, nodes, plans, clock := imageGCExecutionFixture(t)
	rec := imageGCReference(t, nodes)
	if err := c.Apply(nil, &rec); err != nil {
		t.Fatal(err)
	}
	events, err := c.ExecuteImages(plans, clock)
	if err != nil || len(events) != len(nodes) {
		t.Fatalf("referenced execution = %+v, %v", events, err)
	}
	for _, event := range events {
		if event.Action != "skipped" || event.Reason != "changed" {
			t.Fatalf("new reference ignored: %+v", event)
		}
	}
	repo, _ := image.OpenRepo(c.ImagesDir, plans[0].Name)
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != len(nodes) {
		t.Fatalf("referenced versions changed: %+v, %v", manifest, err)
	}
}

func TestExecuteImageGCWaitsForConcurrentReferenceCommit(t *testing.T) {
	c, nodes, plans, clock := imageGCExecutionFixture(t)
	c.Timeout = 5 * time.Second
	rec := imageGCReference(t, nodes)
	entered, proceed := make(chan struct{}), make(chan struct{})
	reference := make(chan error, 1)
	go func() {
		reference <- c.withSessionScope(rec.Name, referenceImageNames(&rec), func(store Store) error {
			close(entered)
			<-proceed
			return store.SaveVersion(rec)
		}, false)
	}()
	select {
	case <-entered:
	case err := <-reference:
		t.Fatalf("reference lock failed: %v", err)
	case <-time.After(5 * time.Second):
		close(proceed)
		t.Fatal("reference lock timed out")
	}
	execution := make(chan error, 1)
	go func() {
		events, err := c.ExecuteImages(plans, clock)
		for _, event := range events {
			if event.Action == image.GCActionRemoved {
				err = errors.New("concurrently referenced version was removed")
			}
		}
		execution <- err
	}()
	close(proceed)
	if err := <-reference; err != nil {
		t.Fatal(err)
	}
	if err := <-execution; err != nil {
		t.Fatal(err)
	}
}

func TestExecuteImageGCDoesNotReplayNewPendingRecovery(t *testing.T) {
	for _, pending := range []string{"image", "session"} {
		t.Run(pending, func(t *testing.T) {
			c, nodes, plans, clock := imageGCExecutionFixture(t)
			repo, _ := image.OpenRepo(c.ImagesDir, plans[0].Name)
			before, _ := os.ReadFile(repo.MetaPath())
			var path string
			if pending == "image" {
				path = filepath.Join(c.ImagesDir, ".version-removal.json")
				if err := os.WriteFile(path, []byte("invalid pending intent"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				rec := imageGCReference(t, nodes)
				if err := c.Apply(nil, &rec); err != nil {
					t.Fatal(err)
				}
				state := mustReferenceState(t, c.Store)
				if err := c.SaveReferenceJournal(ReferenceJournal{Version: ReferenceControlVersion,
					Generation: state.Generation + 1, Before: &rec}); err != nil {
					t.Fatal(err)
				}
				path, _ = c.referencePath("journal.json")
			}
			intent, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if events, err := c.ExecuteImages(plans, clock); err == nil || len(events) != 0 {
				t.Fatalf("pending intent authorized execution: %+v, %v", events, err)
			}
			after, _ := os.ReadFile(repo.MetaPath())
			remaining, err := os.ReadFile(path)
			if !slices.Equal(before, after) || err != nil || !slices.Equal(intent, remaining) {
				t.Fatal("refused execution changed metadata or recovery intent")
			}
		})
	}
}

func TestExecuteImageGCDeletesOnlyApprovedRepositoriesAndReportsDisappearance(t *testing.T) {
	c, nodes, plans, clock := imageGCExecutionFixture(t)
	events, err := c.ExecuteImages(plans, clock)
	if err != nil || len(events) != len(nodes) {
		t.Fatalf("execution = %+v, %v", events, err)
	}
	for _, event := range events {
		if event.Action != image.GCActionRemoved {
			t.Fatalf("expected removal: %+v", event)
		}
	}
	events, err = c.ExecuteImages(plans, clock)
	if err != nil || len(events) != len(nodes) {
		t.Fatalf("repeated execution = %+v, %v", events, err)
	}
	for _, event := range events {
		if event.Reason != "gone" {
			t.Fatalf("disappearance = %+v", event)
		}
	}
	if _, err := os.Stat(filepath.Join(c.ImagesDir, "alice/app")); !os.IsNotExist(err) {
		t.Fatalf("disappeared repository recreated: %v", err)
	}
	if entries, err := os.ReadDir(c.Dir); err == nil {
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), ".") {
				t.Fatalf("image GC created a session: %s", entry.Name())
			}
		}
	}
}
