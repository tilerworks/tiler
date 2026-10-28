package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
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

func TestWriteVersionImageTableRendersCompleteForest(t *testing.T) {
	base := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	root := versionNode("a", image.KindFull, "", base)
	root.Description = "Base runtime\nDetails"
	child := versionNode("b", image.KindIncremental, root.ID, base.Add(time.Hour))
	child.Description = "Add tools"
	branch := versionNode("c", image.KindIncremental, root.ID, base.Add(2*time.Hour))
	branch.Description = ""
	newRoot := versionNode("d", image.KindFull, "", base.Add(3*time.Hour))
	newRoot.Description = "New base"
	view := versionImageView{Name: "alice/app", Latest: &newRoot,
		Images: []image.ImageMeta{root, child, branch, newRoot}}

	var out bytes.Buffer
	if err := writeVersionImageTable(&out, []versionImageView{view}); err != nil {
		t.Fatal(err)
	}
	want := "alice/app  latest=dddddddddd  created=2026-10-28T04:00:00Z\n" +
		"  ID          KIND         CREATED               DESCRIPTION\n" +
		"  aaaaaaaaaa  full         2026-10-28T01:00:00Z  Base runtime\n" +
		"    bbbbbbbbbb  incremental  2026-10-28T02:00:00Z  Add tools\n" +
		"    cccccccccc  incremental  2026-10-28T03:00:00Z  -\n" +
		"  dddddddddd  full         2026-10-28T04:00:00Z  New base\n"
	if out.String() != want {
		t.Fatalf("table = %q, want %q", out.String(), want)
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

func TestVersionRepositoryReadersUseImmutableMetadata(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	root := versionNode("a", image.KindFull, "", base)
	child := versionNode("b", image.KindIncremental, root.ID, base.Add(time.Hour))
	writeVersionForest(t, dir, "alice/app", child, root)
	cfg := config.Config{ImagesDir: dir}

	var out bytes.Buffer
	if err := listAllImages(&out, cfg, false, false); err != nil {
		t.Fatal(err)
	}
	if text := out.String(); !strings.Contains(text, "alice/app") || !strings.Contains(text, child.ID[:10]) {
		t.Fatalf("list output = %q", text)
	}
	out.Reset()
	if err := showOneImage(&out, cfg, "alice/app:"+root.ID[:8], false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "  id          "+root.ID) {
		t.Fatalf("show output = %q", out.String())
	}
	out.Reset()
	if err := listAllImages(&out, cfg, false, true); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc.SchemaVersion != "3.0" {
		t.Fatalf("json = %q, %v", out.String(), err)
	}

	if err := os.WriteFile(filepath.Join(dir, "alice", "app", image.ImageFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := listAllImages(&out, cfg, false, false); err == nil || !strings.Contains(err.Error(), "legacy layout") {
		t.Fatalf("mixed layout error = %v", err)
	}
}
