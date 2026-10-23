package image

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckNamePlacementRejectsNestedLeaves(t *testing.T) {
	cfg := buildConfig(t)
	leaf, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(leaf.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leaf.Dir, fileMeta), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	deeper, err := OpenRepo(cfg.ImagesDir, "alice/app/extra")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkNamePlacement(cfg.ImagesDir, deeper.Segments); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("deeper under leaf: %v", err)
	}

	parent, err := OpenRepo(cfg.ImagesDir, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkNamePlacement(cfg.ImagesDir, parent.Segments); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("ancestor of leaf: %v", err)
	}

	// Republishing the same leaf is fine.
	if err := checkNamePlacement(cfg.ImagesDir, leaf.Segments); err != nil {
		t.Fatal(err)
	}
}

func TestBuildFullRejectsNameConflict(t *testing.T) {
	cfg := buildConfig(t)
	src := sourceDir(t)
	runner := &fakeRunner{body: []byte("leaf")}
	if _, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src, Builder: "test", Run: runner.run,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app/extra", SrcDir: src, Builder: "test", Run: runner.run,
	})
	if !errors.Is(err, ErrNameConflict) {
		t.Fatalf("error = %v, want ErrNameConflict", err)
	}
}

func TestOpenRepoJoinsMultiSegmentNames(t *testing.T) {
	for _, name := range []string{"solo", "a/b/c", "a/b/c/d/e"} {
		repo, err := OpenRepo("/images", name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if repo.Name() != name {
			t.Fatalf("name = %s, want %s", repo.Name(), name)
		}
	}
}
