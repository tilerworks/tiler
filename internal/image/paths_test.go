package image

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestOpenRepoLayout(t *testing.T) {
	repo, err := OpenRepo("/var/lib/images", "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if repo.Owner != "alice" || repo.Repo != "app" {
		t.Fatalf("name = %s/%s", repo.Owner, repo.Repo)
	}
	root := filepath.Join("/var/lib/images", "alice", "app")
	if repo.Dir != root {
		t.Fatalf("dir = %s", repo.Dir)
	}
	if repo.ImagePath() != filepath.Join(root, "image.sqsh") {
		t.Fatalf("image = %s", repo.ImagePath())
	}
	if repo.MetaPath() != filepath.Join(root, "meta.yaml") {
		t.Fatalf("meta = %s", repo.MetaPath())
	}
	const id = "0123456789abcdef"
	incr, err := repo.IncrPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if incr != filepath.Join(root, "_incr", id+".sqsh") {
		t.Fatalf("incr = %s", incr)
	}
	arch, err := repo.ArchivePath(id)
	if err != nil {
		t.Fatal(err)
	}
	if arch != filepath.Join(root, "_archives", id) {
		t.Fatalf("archive = %s", arch)
	}
	hold, err := repo.HoldPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if hold != filepath.Join(root, "_hold", id) {
		t.Fatalf("hold = %s", hold)
	}
}

func TestLibraryPathsRejectEscape(t *testing.T) {
	repo, err := OpenRepo(t.TempDir(), "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	samples := []string{
		"..",
		"../x",
		"/tmp/x",
		"a/../../b",
		"a/../b",
		"",
		".",
		"ABCDEF0123456789",
		"0123456789abcde",
		"image.sqsh",
	}
	for _, id := range samples {
		if _, err := repo.IncrPath(id); err == nil {
			t.Errorf("incr accepted %q", id)
		}
		if _, err := repo.ArchivePath(id); err == nil {
			t.Errorf("archive accepted %q", id)
		}
		if _, err := repo.HoldPath(id); err == nil {
			t.Errorf("hold accepted %q", id)
		}
		if _, err := repo.BlocksDirPath(id); err == nil {
			t.Errorf("blocks dir accepted %q", id)
		}
		if _, err := repo.BlockPath(id, "blk.000"); err == nil {
			t.Errorf("block accepted id %q", id)
		}
	}
	if _, err := OpenRepo("  ", "alice/app"); err == nil {
		t.Fatal("accepted empty images dir")
	}
	if _, err := OpenRepo(t.TempDir(), "alice/app/extra"); err == nil {
		t.Fatal("accepted three segments")
	}
}

func TestCheckRelRejectsDotDot(t *testing.T) {
	for _, rel := range []string{"../x", "/abs", "a/../../b", "a/../b", "", ".", "foo//bar", "foo/"} {
		if err := checkRel(rel); err == nil {
			t.Errorf("accepted %q", rel)
		}
	}
	if err := checkRel("_incr/0123456789abcdef.sqsh"); err != nil {
		t.Fatal(err)
	}
}

func TestSplitPayloadLayout(t *testing.T) {
	repo, err := OpenRepo("/var/lib/images", "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("/var/lib/images", "alice", "app")
	const id = "0123456789abcdef"
	blocksDir, err := repo.BlocksDirPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if blocksDir != filepath.Join(root, "_blocks", id) {
		t.Fatalf("blocks dir = %s", blocksDir)
	}
	block, err := repo.BlockPath(id, "blk.007")
	if err != nil {
		t.Fatal(err)
	}
	if block != filepath.Join(root, "_blocks", id, "blk.007") {
		t.Fatalf("block = %s", block)
	}
	if fileConcatView != "image.sqsh-concat-view" {
		t.Fatalf("concat view = %s", fileConcatView)
	}
	if got := fmt.Sprintf(blockNameFmt, 7); got != "blk.007" {
		t.Fatalf("block name = %s", got)
	}
}

func TestSplitPayloadPathsRejectEscape(t *testing.T) {
	repo, err := OpenRepo(t.TempDir(), "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef"
	blockNames := []string{
		"", ".", "..", "blk.", "blk.00", "blk.0000", "blk.0a0",
		"BLK.000", "../blk.000", "blk.000/../meta.yaml", "blk.000/",
		"meta.yaml",
	}
	for _, name := range blockNames {
		if _, err := repo.BlockPath(id, name); err == nil {
			t.Errorf("block accepted %q", name)
		}
	}
	for _, name := range []string{"blk.000", "blk.999"} {
		if _, err := repo.BlockPath(id, name); err != nil {
			t.Errorf("block rejected %q: %v", name, err)
		}
	}
}
