package image

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func storeVersionIndexFixture(t *testing.T, repo Repo, manifest VersionManifest) {
	t.Helper()
	body, err := EncodeVersionManifest(manifest)
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

func TestVersionIndexesCoverForestAndDescriptionRevisions(t *testing.T) {
	repo, manifest := testRepo(t), versionFixture()
	manifest.Name = repo.Name()
	manifest.Images[0].ContentSHA256 = strings.Repeat("f", 64)
	storeVersionIndexFixture(t, repo, manifest)
	revision, err := EnsureVersionIndexes(repo)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := readIndex(filepath.Join(repo.Dir, fileSHA256Index))
	if err != nil || len(rows) != len(manifest.Images) {
		t.Fatalf("payload index = %#v, %v", rows, err)
	}
	for _, node := range manifest.Images {
		matches := matchIndex(rows, node.ID, false)
		if len(matches) != 1 || matches[0].rel != dirObjects+"/"+node.ID {
			t.Fatalf("missing node %s: %#v", node.ID, matches)
		}
	}
	content := readLeafFile(t, repo, fileContentSHA256Index)
	if content != indexLine(manifest.Images[0].ContentSHA256, dirObjects+"/"+manifest.Images[0].ID) {
		t.Fatalf("content index = %q", content)
	}
	for _, name := range []string{fileSHA256Index, fileContentSHA256Index, fileVersionRevision} {
		before := readLeafFile(t, repo, name)
		if err := os.Remove(filepath.Join(repo.Dir, name)); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureVersionIndexes(repo); err != nil {
			t.Fatal(err)
		}
		assertBytes(t, filepath.Join(repo.Dir, name), []byte(before))
		if err := os.WriteFile(filepath.Join(repo.Dir, name), []byte("stale or partial cache"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := EnsureVersionIndexes(repo); err != nil || got != revision {
			t.Fatalf("cache recovery: %q, %v", got, err)
		}
		assertBytes(t, filepath.Join(repo.Dir, name), []byte(before))
	}
	manifest.Images[1].Description = "Updated description"
	storeVersionIndexFixture(t, repo, manifest)
	if got, err := EnsureVersionIndexes(repo); err != nil || got == revision {
		t.Fatalf("description did not change revision: %q, %v", got, err)
	}
	assertBytes(t, filepath.Join(repo.Dir, fileContentSHA256Index), []byte(content))
	removed := manifest.Images[len(manifest.Images)-1].ID
	manifest.Images = manifest.Images[:len(manifest.Images)-1]
	storeVersionIndexFixture(t, repo, manifest)
	if _, err := EnsureVersionIndexes(repo); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readLeafFile(t, repo, fileSHA256Index), removed) {
		t.Fatal("index retained a removed version")
	}
}

func TestVersionIndexesRejectUnsafeCaches(t *testing.T) {
	for _, marker := range []string{fileSHA256Index, fileContentSHA256Index} {
		repo, manifest := testRepo(t), versionFixture()
		manifest.Name = repo.Name()
		storeVersionIndexFixture(t, repo, manifest)
		path := filepath.Join(repo.Dir, marker)
		if err := os.Symlink(t.TempDir(), path); err != nil {
			t.Fatal(err)
		}
		if _, err := EnsureVersionIndexes(repo); err == nil {
			t.Fatalf("accepted unsafe cache %s", marker)
		}
	}
}

func TestVersionObjectLeafEnumerationAndEmptyIndexes(t *testing.T) {
	repo := testRepo(t)
	if err := os.MkdirAll(filepath.Join(repo.Dir, dirObjects, strings.Repeat("a", 64)), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureVersionIndexes(repo); err == nil {
		t.Fatal("accepted missing manifest")
	}
	repos, err := ListRepos(linkImagesRoot(repo))
	if err != nil || len(repos) != 1 || repos[0].Name() != repo.Name() {
		t.Fatalf("object-only enumeration: %#v, %v", repos, err)
	}
	storeVersionIndexFixture(t, repo, VersionManifest{Version: VersionManifestVersion, Name: repo.Name()})
	if _, err := EnsureVersionIndexes(repo); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(repo.Dir, fileSHA256Index), nil)
}
