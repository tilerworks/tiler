package image

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUpdateVersionDescriptionUsesAtomicComparison(t *testing.T) {
	repo, node := testRepo(t), versionNode("original")
	if _, err := appendFullVersion(repo, node, true, hooks{}); err != nil {
		t.Fatal(err)
	}
	before := node
	changed, err := UpdateVersionDescription(filepathDir(repo), repo.Name(), node.ID,
		"original", "updated\r\n\nDetails\n")
	if err != nil || !changed {
		t.Fatalf("update = %v, %v", changed, err)
	}
	manifest, _, err := loadVersionMeta(repo)
	got := manifest.Images[0]
	got.Description, before.Description = "", ""
	if err != nil || !reflect.DeepEqual(got, before) {
		t.Fatalf("identity changed: %#v, %v", manifest.Images[0], err)
	}
	assertObjectVersionFile(t, repo, manifest.Images[0])
	if changed, err = UpdateVersionDescription(filepathDir(repo), repo.Name(), node.ID,
		"updated\n\nDetails", "updated\n\nDetails"); err != nil || changed {
		t.Fatalf("unchanged = %v, %v", changed, err)
	}
	if _, err = UpdateVersionDescription(filepathDir(repo), repo.Name(), node.ID,
		"original", "conflict"); err == nil {
		t.Fatal("stale comparison succeeded")
	}
	if changed, err = UpdateVersionDescription(filepathDir(repo), repo.Name(), node.ID,
		"updated\n\nDetails", ""); err != nil || !changed {
		t.Fatalf("clear = %v, %v", changed, err)
	}
	manifest, _, err = loadVersionMeta(repo)
	if err != nil {
		t.Fatal(err)
	}
	assertObjectVersionFile(t, repo, manifest.Images[0])
	path, err := repo.objectVersionFilePath(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if changed, err = UpdateVersionDescription(filepathDir(repo), repo.Name(), node.ID, "", ""); err != nil || changed {
		t.Fatalf("repair = %v, %v", changed, err)
	}
	assertObjectVersionFile(t, repo, manifest.Images[0])
}

func filepathDir(repo Repo) string {
	dir := repo.Dir
	for range len(repo.Segments) {
		dir = filepath.Dir(dir)
	}
	return dir
}
