//go:build linux || darwin

package session

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

func TestPlanVersionRemovalFindsOnlyDescendantOwners(t *testing.T) {
	c := newReferenceCoordinator(t)
	ids := removalIDs()
	nodes := []image.ImageMeta{
		removalNode(ids[0], "", image.KindFull, 0),
		removalNode(ids[1], ids[0], image.KindIncremental, 1),
		removalNode(ids[2], ids[1], image.KindIncremental, 2),
		removalNode(ids[3], ids[0], image.KindIncremental, 3),
		removalNode(ids[4], "", image.KindFull, 4),
	}
	records := []Record{
		removalRecord("active", StateActive, ids[2], ids[1]),
		removalRecord("closed", StateClosed, ids[1], ""),
		removalRecord("stale", StateActive, ids[2], ""),
		removalRecord("external", StateActive, ids[2], ""),
		removalRecord("sibling", StateActive, ids[3], ""),
		removalRecord("other-root", StateClosed, ids[4], ""),
	}
	records[3].Actions = append(records[3].Actions, Action{Seq: 2, Type: TypeMount,
		Image: "/objects/image.sqsh", TipID: ids[2], RuntimeID: "m2",
		Mountpoint: "/mnt/external-file", Mode: ModeRO, Status: StatusMounted})
	for i := range records {
		if err := c.Apply(nil, &records[i]); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := c.PlanVersionRemoval("alice/app", nodes, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if got := removalImageIDs(plan.Images); !slices.Equal(got, []string{ids[2], ids[1]}) {
		t.Fatalf("removal order = %v", got)
	}
	if len(plan.Blockers) != 2 || len(plan.Blockers[0].Owners) != 3 ||
		len(plan.Blockers[1].Owners) != 2 {
		t.Fatalf("blockers = %#v", plan.Blockers)
	}
	if plan.Blockers[1].Owners[0].Role != "served" ||
		plan.Blockers[1].Owners[1].Role != "desired" {
		t.Fatalf("blocking roles = %#v", plan.Blockers[1].Owners)
	}
	wantSessions := []string{"active", "external", "stale", "active", "closed"}
	var gotSessions []string
	for _, bucket := range plan.Blockers {
		for _, owner := range bucket.Owners {
			gotSessions = append(gotSessions, owner.Session)
		}
	}
	if !slices.Equal(gotSessions, wantSessions) {
		t.Fatalf("blocking sessions = %v, want %v", gotSessions, wantSessions)
	}
	for _, owner := range plan.Blockers[0].Owners {
		if owner.Session == "external" && owner.ActionID != 1 {
			t.Fatalf("external file action became a blocker: %#v", owner)
		}
	}
}

func TestRemoveVersionSubtreeRejectsReferencedDescendant(t *testing.T) {
	c := newReferenceCoordinator(t)
	nodes := writeRemovalFixture(t, c)
	rec := removalRecord("blocked", StateActive, nodes[1].ID, "")
	if err := c.Apply(nil, &rec); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RemoveVersionSubtree("alice/app", nodes[0].ID); err == nil {
		t.Fatal("referenced subtree removal succeeded")
	}
	repo, _ := image.OpenRepo(c.ImagesDir, "alice/app")
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != len(nodes) {
		t.Fatalf("blocked removal changed manifest = %#v, %v", manifest, err)
	}
}

func TestConcurrentReferenceCreationPrecedesSubtreeRemoval(t *testing.T) {
	c := newReferenceCoordinator(t)
	c.Timeout = time.Second
	nodes := writeRemovalFixture(t, c)
	rec := removalRecord("racing", StateActive, nodes[1].ID, "")
	entered, save := make(chan struct{}), make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		results <- c.WithSession(rec.Name, func(store Store) error {
			close(entered)
			<-save
			return store.Save(rec)
		})
	}()
	<-entered
	go func() {
		defer wg.Done()
		_, err := c.RemoveVersionSubtree("alice/app", nodes[0].ID)
		results <- err
	}()
	close(save)
	wg.Wait()
	close(results)
	var succeeded, blocked int
	for err := range results {
		if err == nil {
			succeeded++
		} else if strings.Contains(err.Error(), "referenced by sessions") {
			blocked++
		} else {
			t.Fatalf("competing mutation = %v", err)
		}
	}
	if succeeded != 1 || blocked != 1 {
		t.Fatalf("race results: succeeded=%d blocked=%d", succeeded, blocked)
	}
}

func writeRemovalFixture(t *testing.T, c ReferenceCoordinator) []image.ImageMeta {
	t.Helper()
	bodies := [][]byte{[]byte("root"), []byte("child")}
	nodes := make([]image.ImageMeta, len(bodies))
	for i, body := range bodies {
		sum := sha256.Sum256(body)
		id := hex.EncodeToString(sum[:])
		parent := ""
		kind := image.KindFull
		if i != 0 {
			parent, kind = nodes[i-1].ID, image.KindIncremental
		}
		nodes[i] = image.ImageMeta{ID: id, SHA256: id, Size: int64(len(body)), Parent: parent,
			Kind: kind, Builder: "test", Format: image.Format{Type: image.FormatSquashfs, Compression: "gzip"},
			CreatedAt: time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC)}
	}
	repo, err := image.OpenRepo(c.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	manifest := image.VersionManifest{Version: image.VersionManifestVersion, Name: repo.Name(), Images: nodes}
	body, err := image.EncodeVersionManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.MetaPath(), body, 0o644); err != nil {
		t.Fatal(err)
	}
	for i, node := range nodes {
		path, _ := repo.ObjectPayloadPath(node)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bodies[i], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return nodes
}

func removalIDs() []string {
	return []string{strings.Repeat("1", 64), strings.Repeat("2", 64),
		strings.Repeat("3", 64), strings.Repeat("4", 64), strings.Repeat("5", 64)}
}

func removalNode(id, parent, kind string, minute int) image.ImageMeta {
	return image.ImageMeta{ID: id, Parent: parent, Kind: kind,
		CreatedAt: time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC)}
}

func removalRecord(name, state, tip, served string) Record {
	a := Action{Seq: 1, Type: TypeMount, Image: "alice/app", Name: "alice/app",
		FullID: strings.Repeat("1", 64), TipID: tip, ServedTip: served,
		RuntimeID: "m1", Mountpoint: "/mnt/" + name, Mode: ModeRO, Status: StatusMounted}
	if served != "" {
		a.ServedID = a.FullID
	}
	digest := sha256.Sum256([]byte(name))
	return Record{Version: VersionRecordVersion, InstanceID: hex.EncodeToString(digest[:]),
		Name: name, State: state, Actions: []Action{a}, CreatedAt: time.Now()}
}

func removalImageIDs(nodes []image.ImageMeta) []string {
	ids := make([]string, len(nodes))
	for i := range nodes {
		ids[i] = nodes[i].ID
	}
	return ids
}
