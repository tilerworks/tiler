package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImmutableObjectPaths(t *testing.T) {
	repo, err := OpenRepo(t.TempDir(), "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 64)
	root := filepath.Join(repo.Dir, dirObjects, id)
	if got, err := repo.ObjectDirPath(id); err != nil || got != root {
		t.Fatalf("object directory: %q, %v", got, err)
	}
	for _, typ := range []string{FormatSquashfs, FormatErofs, FormatDMG} {
		node := ImageMeta{ID: id, Format: Format{Type: typ}}
		if got, err := repo.ObjectPayloadPath(node); err != nil || got != filepath.Join(root, activeImageName(typ)) {
			t.Fatalf("single %s: %q, %v", typ, got, err)
		}
		node.Blocks = []BlockEntry{{Name: "blk.000"}}
		if got, err := repo.ObjectPayloadPath(node); err != nil || got != filepath.Join(root, fileConcatView) {
			t.Fatalf("split %s: %q, %v", typ, got, err)
		}
	}
	if got, err := repo.ObjectBlockPath(id, "blk.001"); err != nil || got != filepath.Join(root, "blk.001") {
		t.Fatalf("block: %q, %v", got, err)
	}
	for _, bad := range []string{"", "../x", "/tmp/x", strings.Repeat("a", 16), strings.Repeat("A", 64)} {
		if _, err := repo.ObjectDirPath(bad); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("accepted invalid ID %q: %v", bad, err)
		}
	}
	for _, bad := range []string{"../blk.000", "blk.000/../meta.yaml", "blk.00", "meta.yaml"} {
		if _, err := repo.ObjectBlockPath(id, bad); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("accepted invalid block %q: %v", bad, err)
		}
	}
	if _, err := repo.ObjectPayloadPath(ImageMeta{ID: id, Format: Format{Type: "unknown"}}); err == nil {
		t.Fatal("accepted unknown container format")
	}
	if _, err := (Repo{}).ObjectDirPath(id); err == nil {
		t.Fatal("accepted empty repository path")
	}
	if _, err := os.Stat(repo.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path construction changed the filesystem: %v", err)
	}
}

func TestObjectPathsRejectNonDirectoryParent(t *testing.T) {
	repo := Repo{Dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(repo.Dir, dirObjects), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ObjectDirPath(strings.Repeat("c", 64)); err == nil {
		t.Fatal("accepted a regular file as an object directory parent")
	}
}

func TestObjectPathsRejectUnsafeComponents(t *testing.T) {
	for _, part := range []string{"alice", "alice/app", "alice/app/_objects", "alice/app/_objects/ID", "alice/app/_objects/ID/image.sqsh", "alice/app/_objects/ID/blk.000"} {
		t.Run(part, func(t *testing.T) {
			library := t.TempDir()
			repo, err := OpenRepo(library, "alice/app")
			if err != nil {
				t.Fatal(err)
			}
			id := strings.Repeat("b", 64)
			unsafe := filepath.Join(library, strings.ReplaceAll(part, "ID", id))
			if err := os.MkdirAll(filepath.Dir(unsafe), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), unsafe); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			var pathErr error
			switch filepath.Base(unsafe) {
			case ImageFile:
				_, pathErr = repo.ObjectPayloadPath(ImageMeta{ID: id, Format: Format{Type: FormatSquashfs}})
			case "blk.000":
				_, pathErr = repo.ObjectBlockPath(id, "blk.000")
			default:
				_, pathErr = repo.ObjectDirPath(id)
			}
			if !errors.Is(pathErr, ErrInvalidPath) {
				t.Fatalf("accepted symlink %s: %v", unsafe, pathErr)
			}
		})
	}
}
