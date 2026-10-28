package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
)

func versionNode(seed, kind, parent string, created time.Time) image.ImageMeta {
	id := strings.Repeat(seed, 64)
	return image.ImageMeta{ID: id, Kind: kind, Parent: parent,
		Format: image.Format{Type: "squashfs", Compression: "zstd"},
		Size:   1, SHA256: id, MtimeUnix: created.Unix(), CreatedAt: created,
		Builder: "test", Description: "description " + seed}
}

func writeVersionForest(t *testing.T, imagesDir, name string, nodes ...image.ImageMeta) {
	t.Helper()
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	body, err := image.EncodeVersionManifest(image.VersionManifest{
		Version: image.VersionManifestVersion, Name: name, Images: nodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.MetaPath(), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeVersionSession(t *testing.T, dir, name, imageName, root, tip string) {
	t.Helper()
	rec, err := session.NewVersionRecord(name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec.Actions = []session.Action{{Seq: 1, Type: session.TypeMount,
		Image: imageName, Name: imageName, FullID: root, TipID: tip,
		RuntimeID: "m1", Mountpoint: "/workspace", Mode: session.ModeRW,
		Status: session.StatusMounted, Timestamp: time.Now()}}
	if err := (session.Store{Dir: dir}).SaveVersion(rec); err != nil {
		t.Fatal(err)
	}
}

func TestCollectVersionImagesPreservesForestMetadata(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	root := versionNode("a", image.KindFull, "", base)
	child := versionNode("b", image.KindIncremental, root.ID, base.Add(time.Hour))
	branch := versionNode("c", image.KindIncremental, root.ID, base.Add(2*time.Hour))
	newRoot := versionNode("d", image.KindFull, "", base.Add(3*time.Hour))
	writeVersionForest(t, dir, "alice/app", branch, newRoot, child, root)

	views, err := collectVersionImages(dir)
	if err != nil || len(views) != 1 {
		t.Fatalf("views = %#v, %v", views, err)
	}
	view := views[0]
	want := []string{root.ID, child.ID, branch.ID, newRoot.ID}
	for i, node := range view.Images {
		if node.ID != want[i] || node.Description == "" || node.Format.Compression != "zstd" {
			t.Fatalf("node %d = %#v", i, node)
		}
	}
	if view.Latest == nil || view.Latest.ID != newRoot.ID {
		t.Fatalf("latest = %#v", view.Latest)
	}
}

func TestCollectVersionShowPinsSelectedAncestorStack(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC()
	root := versionNode("a", image.KindFull, "", base)
	child := versionNode("b", image.KindIncremental, root.ID, base.Add(time.Minute))
	branch := versionNode("c", image.KindIncremental, root.ID, base.Add(2*time.Minute))
	writeVersionForest(t, dir, "alice/app", branch, child, root)
	writeVersionSession(t, filepath.Join(dir, "sessions"), "dev", "alice/app", root.ID, child.ID)

	view, err := collectVersionShow(dir, "alice/app:"+child.ID[:10])
	if err != nil {
		t.Fatal(err)
	}
	if view.Selected == nil || view.Selected.ID != child.ID || len(view.Stack) != 2 || view.Stack[0].ID != root.ID {
		t.Fatalf("show view = %#v", view)
	}
}

func TestCollectVersionImagesRejectsLegacyMetadata(t *testing.T) {
	dir := t.TempDir()
	repo, err := image.OpenRepo(dir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.MetaPath(), []byte("version: '2.0'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := collectVersionImages(dir); err == nil || !strings.Contains(err.Error(), "unsupported metadata version") {
		t.Fatalf("legacy error = %v", err)
	}
}
