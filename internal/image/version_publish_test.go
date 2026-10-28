package image

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFullVersionObjectsPreserveHistoryAndStaging(t *testing.T) {
	repo := testRepo(t)
	root, old := filepath.Dir(filepath.Dir(repo.Dir)), versionNode("old")
	for _, node := range []ImageMeta{old, versionNode("new")} {
		stage := writeSource(t, t.TempDir(), []byte(node.Description))
		if _, err := PublishFullVersion(root, repo.Name(), stage, node, true); err != nil {
			t.Fatal(err)
		}
		assertBytes(t, stage, []byte(node.Description))
	}
	oldPath := filepath.Join(repo.Dir, dirObjects, old.ID, ImageFile)
	duplicate := old
	duplicate.CreatedAt, duplicate.Description = old.CreatedAt.Add(time.Hour), "ignored"
	stage := writeSource(t, t.TempDir(), []byte("old"))
	got, err := PublishFullVersion(root, repo.Name(), stage, duplicate, false)
	if err != nil || !got.CreatedAt.Equal(old.CreatedAt) || got.Description != old.Description {
		t.Fatalf("duplicate changed node: %#v, %v", got, err)
	}
	if err := quickCheckFile(oldPath, got.Size, got.MtimeUnix); err != nil {
		t.Fatalf("published metadata does not match object: %v", err)
	}
	if _, err := PublishFullVersion(root, repo.Name(), stage, duplicate, true); err == nil {
		t.Fatal("accepted conflicting explicit description")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(oldPath), alias); err != nil {
		t.Fatal(err)
	}
	if _, err := PublishFullVersion(root, "bob/app", filepath.Join(alias, ImageFile), old, false); err == nil {
		t.Fatal("accepted staging alias to a published payload")
	}
	assertBytes(t, oldPath, []byte("old"))
	manifest, _, err := loadVersionMeta(repo)
	if err != nil || len(manifest.Images) != 2 {
		t.Fatalf("history lost: %#v, %v", manifest, err)
	}
}

func TestFullVersionObjectsRejectUnsafeOrFailedPublication(t *testing.T) {
	for _, failure := range []string{"digest", "orphan", "object stage", "symlink stage", "metadata", "object sync"} {
		t.Run(failure, func(t *testing.T) {
			repo, node := testRepo(t), versionNode("new")
			stage := writeSource(t, t.TempDir(), []byte("new"))
			dst, err := repo.ObjectPayloadPath(node)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				t.Fatal(err)
			}
			seam := hooks{}
			switch failure {
			case "digest":
				node.Size++
			case "orphan", "object stage":
				if err := os.WriteFile(dst, []byte("bad"), 0o644); err != nil {
					t.Fatal(err)
				}
				if failure == "object stage" {
					stage = dst
				}
			case "symlink stage":
				link := filepath.Join(t.TempDir(), "link")
				if err := os.Symlink(stage, link); err != nil {
					t.Fatal(err)
				}
				stage = link
			case "metadata":
				seam.before = func(string, string) error { return errors.New("metadata failure") }
			case "object sync":
				seam.sync = func(string) error { return errors.New("object sync failure") }
			}
			if _, err := publishFullVersion(linkImagesRoot(repo), repo, stage, node, false, seam); err == nil {
				t.Fatal("accepted failed publication")
			}
			if _, err := os.Stat(repo.MetaPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed publication exposed metadata: %v", err)
			}
			if _, err := os.Lstat(stage); err != nil {
				t.Fatalf("caller lost staged input: %v", err)
			}
		})
	}
}
