package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func stagedSplitVersion(t *testing.T, text string, size int64) (string, ImageMeta) {
	t.Helper()
	stage, dir := writeSource(t, t.TempDir(), []byte(text)), t.TempDir()
	split, err := splitStagedImage(stage, dir, size)
	if err != nil {
		t.Fatal(err)
	}
	node := versionNode(text)
	node.Blocks = split.blocks
	return dir, node
}

func TestVersionPublicationMixesPerNodeSplitModes(t *testing.T) {
	repo := testRepo(t)
	var parent ImageMeta
	for i, size := range []int64{0, 2, 3} {
		text := fmt.Sprintf("payload%d", i)
		stage, node := writeSource(t, t.TempDir(), []byte(text)), versionNode(text)
		if size != 0 {
			stage, node = stagedSplitVersion(t, text, size)
		}
		var got ImageMeta
		var err error
		if i == 0 {
			got, err = PublishFullVersion(linkImagesRoot(repo), repo.Name(), stage, node, false)
		} else {
			node.Kind, node.Parent = KindIncremental, parent.ID
			got, err = PublishIncrementalVersion(linkImagesRoot(repo), repo.Name(), stage, node, parent, false)
		}
		if err != nil {
			t.Fatal(err)
		}
		parent = got
		for _, block := range got.Blocks {
			path := filepath.Join(repo.Dir, dirObjects, got.ID, block.Name)
			assertBytes(t, path, []byte(readLeafFile(t, Repo{Dir: stage}, block.Name)))
			if err := quickCheckFile(path, block.Size, block.MtimeUnix); err != nil {
				t.Fatal(err)
			}
		}
		if size != 0 {
			assertBytes(t, filepath.Join(repo.Dir, dirObjects, got.ID, fileConcatView), []byte(readLeafFile(t, Repo{Dir: stage}, fileConcatView)))
		}
	}
	m, _, err := loadVersionMeta(repo)
	if err != nil || len(m.Images) != 3 {
		t.Fatalf("lost split history: %#v, %v", m, err)
	}
	assertBytes(t, filepath.Join(repo.Dir, dirObjects, m.Images[0].ID, ImageFile), []byte("payload0"))
}

func TestSplitVersionsRejectCorruptionAndRecoverOrphans(t *testing.T) {
	for _, failure := range []string{"block", "whole", "descriptor", "orphan", "metadata", "sync"} {
		t.Run(failure, func(t *testing.T) {
			repo := testRepo(t)
			stage, node := stagedSplitVersion(t, "payload", 3)
			seam := hooks{}
			switch failure {
			case "block", "descriptor", "orphan":
				path := filepath.Join(stage, node.Blocks[0].Name)
				if failure == "descriptor" {
					path = filepath.Join(stage, fileConcatView)
				}
				if failure == "orphan" {
					path = filepath.Join(repo.Dir, dirObjects, node.ID, node.Blocks[0].Name)
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(path, []byte("bad"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "whole":
				node.ID, node.SHA256 = versionNode("other").ID, versionNode("other").SHA256
			case "metadata":
				seam.before = func(string, string) error { return errors.New("metadata failure") }
			case "sync":
				seam.sync = func(string) error { return errors.New("sync failure") }
			}
			if _, err := publishFullVersion(linkImagesRoot(repo), repo, stage, node, false, seam); err == nil {
				t.Fatal("accepted invalid split publication")
			}
			if _, err := os.Stat(repo.MetaPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failure exposed metadata: %v", err)
			}
			if failure == "metadata" || failure == "sync" {
				if _, err := PublishFullVersion(linkImagesRoot(repo), repo.Name(), stage, node, false); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
