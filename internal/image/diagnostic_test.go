package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingManifestPreservesCauseAndRepositoryState(t *testing.T) {
	root := t.TempDir()
	repo, err := OpenRepo(root, "my/images")
	if err != nil {
		t.Fatal(err)
	}
	for _, exists := range []bool{false, true} {
		if exists {
			if err := os.MkdirAll(repo.Dir, 0755); err != nil {
				t.Fatal(err)
			}
		}
		_, err := LoadVersionManifest(repo)
		var missing *MissingManifestError
		if !errors.Is(err, os.ErrNotExist) || !errors.As(err, &missing) {
			t.Fatalf("lost error identity: %v", err)
		}
		if missing.RepositoryExists != exists || missing.Library != root || missing.Name != "my/images" || missing.Path != filepath.Join(repo.Dir, "meta.yaml") {
			t.Fatalf("wrong diagnostic: %+v", missing)
		}
	}
}

func TestCorruptMetadataRetainsImageAndPath(t *testing.T) {
	repo, err := OpenRepo(t.TempDir(), "my/images")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.MetaPath(), []byte("[invalid"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadVersionManifest(repo)
	if err == nil || errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), repo.MetaPath()) || !strings.Contains(err.Error(), `image "my/images"`) {
		t.Fatalf("wrong corruption diagnostic: %v", err)
	}
}
