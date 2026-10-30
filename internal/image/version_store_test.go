package image

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func assertObjectVersionFile(t *testing.T, repo Repo, node ImageMeta) {
	t.Helper()
	path, err := repo.objectVersionFilePath(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	file, err := DecodeObjectVersionFile(mustRead(t, path))
	if err != nil || file.Version != VersionManifestVersion || file.Name != repo.Name() || !reflect.DeepEqual(file.Image, node) {
		t.Fatalf("version file = %#v, node %#v, %v", file, node, err)
	}
}

func versionNode(text string) ImageMeta {
	m := manifestFor([]byte(text))
	node := rootOf(m)
	node.Format, node.Description = m.Format, text
	return node
}

func TestVersionMetadataAppendPreservesHistoryAndIdentity(t *testing.T) {
	repo, old := testRepo(t), versionNode("old")
	if _, err := appendFullVersion(repo, old, true, hooks{}); err != nil {
		t.Fatal(err)
	}
	duplicate := old
	duplicate.CreatedAt, duplicate.Description = old.CreatedAt.Add(time.Hour), "ignored"
	got, err := appendFullVersion(repo, duplicate, false, hooks{})
	if err != nil || !reflect.DeepEqual(got, old) {
		t.Fatalf("duplicate changed publication facts: %#v, %v", got, err)
	}
	if _, err := appendFullVersion(repo, duplicate, true, hooks{}); err == nil {
		t.Fatal("accepted conflicting explicit description")
	}
	duplicate.Format = Format{Type: FormatErofs, Compression: "lz4"}
	if _, err := appendFullVersion(repo, duplicate, false, hooks{}); err == nil {
		t.Fatal("accepted conflicting payload identity")
	}
	if _, err := appendFullVersion(repo, versionNode("new"), true, hooks{}); err != nil {
		t.Fatal(err)
	}
	m, _, err := loadVersionMeta(repo)
	if err != nil || len(m.Images) != 2 || !reflect.DeepEqual(m.Images[0], old) {
		t.Fatalf("history changed: %#v, %v", m, err)
	}
	for _, node := range m.Images {
		assertObjectVersionFile(t, repo, node)
	}
	removed, err := repo.objectVersionFilePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	if _, err := appendFullVersion(repo, old, false, hooks{}); err != nil {
		t.Fatal(err)
	}
	assertObjectVersionFile(t, repo, old)
}

func TestVersionMetadataFailureRestoresPreviousDocument(t *testing.T) {
	fail := func(string) error { return errors.New("injected failure") }
	for _, seam := range []hooks{
		{before: func(string, string) error { return fail("") }},
		{rename: func(string, string) error { return fail("") }},
		{sync: fail},
	} {
		for _, existing := range []bool{false, true} {
			repo := testRepo(t)
			if existing {
				if _, err := appendFullVersion(repo, versionNode("old"), false, hooks{}); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(repo.MetaPath())
			if _, err := appendFullVersion(repo, versionNode("new"), false, seam); err == nil {
				t.Fatal("expected injected failure")
			}
			after, _ := os.ReadFile(repo.MetaPath())
			if string(before) != string(after) {
				t.Fatal("failed publish changed previous metadata")
			}
			fresh := versionNode("new")
			freshPath, pathErr := repo.objectVersionFilePath(fresh.ID)
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			if _, statErr := os.Stat(freshPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed publish left %s: %v", freshPath, statErr)
			}
			if existing {
				manifest, _, loadErr := loadVersionMeta(repo)
				if loadErr != nil || len(manifest.Images) != 1 {
					t.Fatalf("previous version file: %#v, %v", manifest, loadErr)
				}
				assertObjectVersionFile(t, repo, manifest.Images[0])
			}
		}
	}
}

func TestVersionMetadataRejectsLegacyLibraries(t *testing.T) {
	for _, marker := range []string{fileMeta, ImageFile, ImageDMG, dirIncr, dirBlocks} {
		repo := testRepo(t)
		if _, err := appendFullVersion(repo, versionNode("old"), false, hooks{}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo.Dir, marker), []byte("version: '2.0'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := readLeafFile(t, repo, fileMeta)
		if _, err := appendFullVersion(repo, versionNode("new"), false, hooks{}); err == nil {
			t.Fatalf("accepted legacy marker %s", marker)
		}
		assertBytes(t, repo.MetaPath(), []byte(before))
	}
}
