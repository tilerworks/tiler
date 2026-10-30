package image

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIncrementalVersionsRetainHistoricalBranches(t *testing.T) {
	repo, base := testRepo(t), versionNode("base")
	root := linkImagesRoot(repo)
	stage := writeSource(t, t.TempDir(), []byte("base"))
	if _, err := PublishFullVersion(root, logsFor(root), repo.Name(), stage, base, false); err != nil {
		t.Fatal(err)
	}
	parent := base
	for _, text := range []string{"child", "grandchild", "sibling"} {
		if text == "sibling" {
			parent = base
		}
		node := versionNode(text)
		node.Kind, node.Parent = KindIncremental, parent.ID
		stage = writeSource(t, t.TempDir(), []byte(text))
		parent.Description = "mutable publication text"
		if _, err := PublishIncrementalVersion(root, logsFor(root), repo.Name(), stage, node, parent, false); err != nil {
			t.Fatal(err)
		}
		parent = node
	}
	m, _, err := loadVersionMeta(repo)
	if err != nil || len(m.Images) != 4 {
		t.Fatalf("branch history: %#v, %v", m, err)
	}
	f, err := NewForest(m.Images)
	if err != nil {
		t.Fatal(err)
	}
	for i, length := range map[int]int{2: 3, 3: 2} {
		stack, err := f.Stack(m.Images[i].ID)
		if err != nil || len(stack) != length || stack[0].ID != base.ID {
			t.Fatalf("wrong ancestry: %#v, %v", stack, err)
		}
	}
	for _, node := range m.Images {
		path, err := repo.ObjectPayloadPath(node)
		if err != nil {
			t.Fatal(err)
		}
		assertBytes(t, path, []byte(node.Description))
	}
}

func TestIncrementalVersionsRejectStaleOrInvalidParents(t *testing.T) {
	for _, failure := range []string{"removed", "identity", "kind", "edge"} {
		t.Run(failure, func(t *testing.T) {
			repo, base := testRepo(t), versionNode("base")
			if _, err := appendFullVersion(repo, base, false, hooks{}); err != nil {
				t.Fatal(err)
			}
			node := versionNode("child")
			node.Kind, node.Parent = KindIncremental, base.ID
			switch failure {
			case "removed":
				body, err := EncodeVersionManifest(VersionManifest{Version: VersionManifestVersion, Name: repo.Name()})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(repo.MetaPath(), body, 0o644); err != nil {
					t.Fatal(err)
				}
			case "identity":
				base.Format = Format{Type: FormatErofs, Compression: "lz4"}
			case "kind":
				node.Kind = KindFull
			case "edge":
				node.Parent = node.ID
			}
			before := readLeafFile(t, repo, fileMeta)
			stage := writeSource(t, t.TempDir(), []byte("child"))
			if _, err := PublishIncrementalVersion(linkImagesRoot(repo), logsFor(linkImagesRoot(repo)), repo.Name(), stage, node, base, false); err == nil {
				t.Fatal("accepted stale or invalid parent")
			}
			assertBytes(t, repo.MetaPath(), []byte(before))
			if _, err := os.Stat(filepath.Join(repo.Dir, dirObjects, node.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected parent placed object: %v", err)
			}
		})
	}
}

func TestIncrementalPublicationRetriesAfterMetadataFailure(t *testing.T) {
	repo, base := testRepo(t), versionNode("base")
	if _, err := appendFullVersion(repo, base, false, hooks{}); err != nil {
		t.Fatal(err)
	}
	node := versionNode("child")
	node.Kind, node.Parent = KindIncremental, base.ID
	stage := writeSource(t, t.TempDir(), []byte("child"))
	before := readLeafFile(t, repo, fileMeta)
	seam := hooks{before: func(string, string) error { return errors.New("metadata failure") }}
	if _, err := publishVersion(linkImagesRoot(repo), repo, stage, node, &base, false, seam); err == nil {
		t.Fatal("expected metadata failure")
	}
	assertBytes(t, repo.MetaPath(), []byte(before))
	if _, err := PublishIncrementalVersion(linkImagesRoot(repo), logsFor(linkImagesRoot(repo)), repo.Name(), stage, node, base, false); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, stage, []byte("child"))
}
