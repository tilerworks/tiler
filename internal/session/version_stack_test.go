package session

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

func sessionVersionManifest() image.VersionManifest {
	m := image.VersionManifest{Version: image.VersionManifestVersion, Name: "alice/app"}
	for i, letter := range []string{"a", "b", "c"} {
		node := image.ImageMeta{ID: strings.Repeat(letter, 64), SHA256: strings.Repeat(letter, 64),
			Kind: image.KindFull, Format: image.Format{Type: image.FormatSquashfs, Compression: "zstd"},
			Size: 1, MtimeUnix: 1, CreatedAt: testNow.Add(time.Duration(i) * time.Second), Builder: "test"}
		if i > 0 {
			node.Kind, node.Parent = image.KindIncremental, m.Images[i-1].ID
			node.Format = image.Format{Type: image.FormatErofs, Compression: "lz4"}
		}
		m.Images = append(m.Images, node)
	}
	m.Images[2].Blocks = []image.BlockEntry{{Name: "blk.000", Size: 1, SHA256: m.Images[2].ID, MtimeUnix: 1}}
	return m
}

func saveSessionVersionManifest(t *testing.T, root string, m image.VersionManifest) image.Repo {
	t.Helper()
	repo, err := image.OpenRepo(root, m.Name)
	if err != nil {
		t.Fatal(err)
	}
	body, err := image.EncodeVersionManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.MetaPath(), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestVersionStackReopensRecordedDesiredAndServedHistory(t *testing.T) {
	root, m := t.TempDir(), sessionVersionManifest()
	repo := saveSessionVersionManifest(t, root, m)
	rec := versionRecord(t)
	a := &rec.Actions[0]
	a.FullID, a.TipID, a.ServedID, a.ServedTip = m.Images[0].ID, m.Images[2].ID, m.Images[0].ID, m.Images[1].ID
	store := Store{Dir: t.TempDir()}
	if err := store.SaveVersion(rec); err != nil {
		t.Fatal(err)
	}
	want, err := a.VersionStack(root, false)
	if err != nil || len(want) != 3 || !strings.HasSuffix(want[2].Path, "image.sqsh-concat-view") {
		t.Fatalf("desired stack = %#v, %v", want, err)
	}
	for i, node := range want {
		if node.Meta.ID != m.Images[i].ID || !strings.Contains(node.Path, filepath.Join("_objects", node.Meta.ID)) {
			t.Fatalf("wrong member: %#v", node)
		}
	}
	newRoot, branch := m.Images[0], m.Images[1]
	newRoot.ID, newRoot.SHA256, newRoot.CreatedAt = strings.Repeat("d", 64), strings.Repeat("d", 64), testNow.Add(time.Hour)
	branch.ID, branch.SHA256, branch.CreatedAt = strings.Repeat("e", 64), strings.Repeat("e", 64), testNow.Add(2*time.Hour)
	m.Images = append(m.Images, newRoot, branch)
	saveSessionVersionManifest(t, root, m)
	reopened, err := store.LoadVersion(rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Actions[0].VersionStack(root, false)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("history moved: %#v, %v", got, err)
	}
	served, err := reopened.Actions[0].VersionStack(root, true)
	if err != nil || !reflect.DeepEqual(served, want[:2]) {
		t.Fatalf("served stack = %#v, %v", served, err)
	}
	a.ServedID, a.ServedTip = "", ""
	if got, err := a.VersionStack(root, true); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("desired fallback = %#v, %v", got, err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(repo.Dir, "_objects")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.VersionStack(root, false); err == nil {
		t.Fatal("followed unsafe payload path")
	}
}

func TestVersionStackRejectsInvalidPinsAndLayouts(t *testing.T) {
	for _, kind := range []string{"missing tip", "short tip", "wrong root", "missing repo", "wrong name", "legacy", "external", "served root"} {
		t.Run(kind, func(t *testing.T) {
			root, m := t.TempDir(), sessionVersionManifest()
			repo := saveSessionVersionManifest(t, root, m)
			a := versionRecord(t).Actions[0]
			a.FullID, a.TipID, a.ServedID, a.ServedTip = m.Images[0].ID, m.Images[2].ID, "", ""
			switch kind {
			case "missing tip":
				a.TipID = strings.Repeat("f", 64)
			case "short tip":
				a.TipID = "aaaa"
			case "wrong root":
				a.FullID = m.Images[1].ID
			case "missing repo":
				a.Name = "bob/app"
			case "wrong name":
				body, _ := image.EncodeVersionManifest(image.VersionManifest{Version: m.Version, Name: "bob/app", Images: m.Images})
				_ = os.WriteFile(repo.MetaPath(), body, 0o644)
			case "legacy":
				_ = os.Mkdir(filepath.Join(repo.Dir, "_hold"), 0o755)
			case "external":
				a.Name, a.FullID, a.Image = "", "", filepath.Join(t.TempDir(), "external.sqsh")
			case "served root":
				a.ServedID, a.ServedTip = m.Images[1].ID, m.Images[2].ID
			}
			if _, err := a.VersionStack(root, true); err == nil {
				t.Fatal("accepted invalid pin or layout")
			}
		})
	}
}
