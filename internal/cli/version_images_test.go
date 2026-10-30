package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func writeVersionSession(t *testing.T, dir, name, imageName, root, tip string) session.Record {
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
	return rec
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

func TestImagesDescribePinsVersionAndClearsDescription(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	node := versionNode("a", image.KindFull, "", time.Now().UTC())
	writeVersionForest(t, dir, "alice/app", node)
	rec := writeVersionSession(t, sessions, "dev", "alice/app", node.ID, node.ID)
	store := session.Store{Dir: sessions}
	wantOwners := []session.ReferenceOwner{{Session: rec.Name, InstanceID: rec.InstanceID,
		ActionID: rec.Actions[0].Seq, Role: "desired"}}
	if err := store.SaveReferenceBucket(session.ReferenceBucket{
		Name: "alice/app", Tip: node.ID, Owners: wantOwners,
	}); err != nil {
		t.Fatal(err)
	}
	cmd := newImagesDescribeCmd()
	cmd.SetContext(context.Background())
	remember(cmd, config.Config{ImagesDir: dir, SessionsDir: sessions})
	cmd.SetArgs([]string{"alice/app:" + node.ID[:4], "-F", "-"})
	cmd.SetIn(strings.NewReader(""))
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	view, err := collectVersionShow(dir, "alice/app:"+node.ID)
	if err != nil || view.Selected.Description != "" {
		t.Fatalf("description = %q, %v", view.Selected.Description, err)
	}
	if out.String() != "Updated description for alice/app:aaaaaaaaaa.\n" {
		t.Fatalf("output = %q", out.String())
	}
	bucket, err := store.LoadReferenceBucket("alice/app", node.ID)
	if err != nil || !reflect.DeepEqual(bucket.Owners, wantOwners) {
		t.Fatalf("reference owners = %#v, %v", bucket.Owners, err)
	}

	out.Reset()
	cmd = newImagesDescribeCmd()
	cmd.SetContext(context.Background())
	remember(cmd, config.Config{ImagesDir: dir, SessionsDir: sessions})
	cmd.SetArgs([]string{"alice/app:" + node.ID[:4], "-F", "-"})
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Description unchanged for alice/app:aaaaaaaaaa.\n" {
		t.Fatalf("output = %q", out.String())
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
		"  ID              SIZE  CREATED               DESCRIPTION\n" +
		"  aaaaaaaaaa      1 B   2026-10-28T01:00:00Z  Base runtime\n" +
		"    ↳ bbbbbbbbbb  1 B   2026-10-28T02:00:00Z  Add tools\n" +
		"    ↳ cccccccccc  1 B   2026-10-28T03:00:00Z  -\n" +
		"  dddddddddd      1 B   2026-10-28T04:00:00Z  New base\n"
	if out.String() != want {
		t.Fatalf("table = %q, want %q", out.String(), want)
	}
}

func TestWriteVersionImageTableReportsShortIDCollisions(t *testing.T) {
	base := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	one := versionNode("a", image.KindFull, "", base)
	two := versionNode("b", image.KindFull, "", base.Add(time.Hour))
	two.ID = one.ID[:10] + two.ID[10:]
	var out bytes.Buffer
	view := versionImageView{Name: "alice/app", Latest: &two,
		Images: []image.ImageMeta{one, two}}
	if err := writeVersionImageTable(&out, []versionImageView{view}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out.String(),
		"Short IDs collide; use images list --json for full IDs.\n") {
		t.Fatalf("collision output = %q", out.String())
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

func TestImagesRemoveVersionSubtreeAndRepository(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC()
	root := versionNode("a", image.KindFull, "", base)
	child := versionNode("b", image.KindIncremental, root.ID, base.Add(time.Minute))
	branch := versionNode("c", image.KindFull, "", base.Add(2*time.Minute))
	writeVersionForest(t, dir, "alice/app", branch, child, root)
	cmd := newImagesRemoveCmd()
	cmd.SetContext(context.Background())
	remember(cmd, config.Config{ImagesDir: dir, SessionsDir: filepath.Join(dir, "sessions")})
	cmd.SetArgs([]string{"alice/app:" + root.ID[:4]})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := "Removed 2 image versions from alice/app:\n  bbbbbbbbbb\n  aaaaaaaaaa\n"; out.String() != want {
		t.Fatalf("version removal = %q, want %q", out.String(), want)
	}
	out.Reset()
	cmd = newImagesRemoveCmd()
	cmd.SetContext(context.Background())
	remember(cmd, config.Config{ImagesDir: dir, SessionsDir: filepath.Join(dir, "sessions")})
	cmd.SetArgs([]string{"alice/app"})
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := "Removed image repository alice/app (1 image versions).\n"; out.String() != want {
		t.Fatalf("repository removal = %q, want %q", out.String(), want)
	}
}

func TestImagesRemoveReportsBlockingSessionsWithoutDeleting(t *testing.T) {
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	root := versionNode("a", image.KindFull, "", time.Now().UTC())
	child := versionNode("b", image.KindIncremental, root.ID, time.Now().UTC())
	writeVersionForest(t, dir, "alice/app", root, child)
	rec, err := session.NewVersionRecord("dev", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rec.State = session.StateClosed
	rec.Actions = []session.Action{{Seq: 1, Type: session.TypeMount, Name: "alice/app",
		Image: "alice/app", FullID: root.ID, TipID: child.ID, RuntimeID: "m1",
		Mountpoint: "/workspace", Mode: session.ModeRW, Status: session.StatusMounted}}
	coordinator := session.ReferenceCoordinator{
		Store: session.Store{Dir: sessionsDir}, ImagesDir: dir,
	}
	if err := coordinator.Apply(nil, &rec); err != nil {
		t.Fatal(err)
	}

	cmd := newImagesRemoveCmd()
	cmd.SetContext(context.Background())
	remember(cmd, config.Config{ImagesDir: dir, SessionsDir: sessionsDir})
	cmd.SetArgs([]string{"alice/app:" + root.ID[:4]})
	err = cmd.Execute()
	want := "cannot remove image versions from alice/app: referenced by sessions\n" +
		"  dev   bbbbbbbbbb  (closed)\nNo image versions were removed."
	if err == nil || err.Error() != want {
		t.Fatalf("blocking error = %q, want %q", err, want)
	}
	view, loadErr := collectVersionShow(dir, "alice/app:"+child.ID)
	if loadErr != nil || view.Selected.ID != child.ID {
		t.Fatalf("blocked removal changed repository: %#v, %v", view, loadErr)
	}
}

func TestImagesListRejectsLegacyArchivesFlag(t *testing.T) {
	cmd := newImagesListCmd()
	cmd.SetArgs([]string{"--archives"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag: --archives") {
		t.Fatalf("archives error = %v", err)
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
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc.SchemaVersion != 1 {
		t.Fatalf("json = %q, %v", out.String(), err)
	}

	if err := os.WriteFile(filepath.Join(dir, "alice", "app", image.ImageFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := listAllImages(&out, cfg, false, false); err == nil || !strings.Contains(err.Error(), "legacy layout") {
		t.Fatalf("mixed layout error = %v", err)
	}
}

func TestVersionImageOutputGoldens(t *testing.T) {
	base := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	root := versionNode("a", image.KindFull, "", base)
	root.Description = "Original base"
	child := versionNode("b", image.KindIncremental, root.ID, base.Add(time.Hour))
	child.Format = image.Format{Type: "erofs", Compression: "lz4hc"}
	child.Description = "Install tools\nPinned toolchain"
	grandchild := versionNode("c", image.KindIncremental, child.ID, base.Add(2*time.Hour))
	grandchild.Blocks = []image.BlockEntry{{}}
	grandchild.Description = "Application layer\nBuilt from release sources"
	branch := versionNode("d", image.KindIncremental, root.ID, base.Add(4*time.Hour))
	branch.Description = "Newer historical branch"
	collision := versionNode("e", image.KindFull, "", base.Add(3*time.Hour))
	collision.ID = root.ID[:10] + collision.ID[10:]
	collision.SHA256 = collision.ID
	collision.Description = "Colliding root"
	view := versionImageView{Name: "alice/app", Latest: &branch,
		Images: []image.ImageMeta{root, child, grandchild, branch, collision}}

	var out bytes.Buffer
	if err := writeVersionImageTable(&out, []versionImageView{view}); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "version_images_list", out.Bytes())
	out.Reset()
	if err := writeVersionImagesJSON(&out, []versionImageView{view}); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "version_images_list_json", out.Bytes())

	view.Selected = &grandchild
	view.Stack = []image.ImageMeta{root, child, grandchild}
	out.Reset()
	if err := writeVersionImageDetail(&out, view); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "version_images_show", out.Bytes())
}

func TestVersionImageEmptyLibraryGoldens(t *testing.T) {
	var out bytes.Buffer
	if err := writeVersionImageTable(&out, nil); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "version_images_empty", out.Bytes())
	out.Reset()
	if err := writeVersionImagesJSON(&out, nil); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "version_images_empty_json", out.Bytes())
}

func TestVersionImageTableAlignsAcrossRepositories(t *testing.T) {
	at := time.Date(2026, 10, 28, 1, 0, 0, 123456789, time.UTC)
	root := versionNode("a", image.KindFull, "", at)
	root.Size = 1024 * 1024
	child := versionNode("b", image.KindIncremental, root.ID, at.Truncate(time.Second))
	child.Size = 1024
	grandchild := versionNode("c", image.KindIncremental, child.ID, at)
	grandchild.Size = 0
	grandchild.Blocks = []image.BlockEntry{{}}
	other := versionNode("d", image.KindFull, "", at)
	other.Size = 42
	views := []versionImageView{
		{Name: "one", Latest: &grandchild, Images: []image.ImageMeta{root, child, grandchild}},
		{Name: "empty"},
		{Name: "two", Latest: &other, Images: []image.ImageMeta{other}},
	}
	var out bytes.Buffer
	if err := writeVersionImageTable(&out, views); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\t") || !strings.Contains(out.String(), "empty  latest=-\n  No image versions.\n") {
		t.Fatalf("unexpected table: %q", out.String())
	}
	var starts []int
	for _, line := range strings.Split(out.String(), "\n") {
		if !strings.HasPrefix(line, "  ") || strings.Contains(line, "No image") {
			continue
		}
		line = strings.ReplaceAll(line, "↳", "x")
		if strings.HasSuffix(line, " ") {
			t.Fatalf("trailing padding: %q", line)
		}
		var columns []int
		if strings.Contains(line, "DESCRIPTION") {
			columns = []int{strings.Index(line, "SIZE"), strings.Index(line, "CREATED"), strings.Index(line, "DESCRIPTION")}
		} else {
			var size string
			for _, candidate := range []string{"1 MiB", "1 KiB", "0 B", "42 B"} {
				if strings.Contains(line, candidate) {
					size = candidate
				}
			}
			columns = []int{strings.Index(line, size), strings.Index(line, "2026-"), strings.Index(line, "description")}
		}
		if starts == nil {
			starts = columns
		} else if !reflect.DeepEqual(starts, columns) {
			t.Fatalf("columns %v differ from %v in %q", columns, starts, line)
		}
	}
}
