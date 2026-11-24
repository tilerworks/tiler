package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/mountinfo"
)

func TestResolveDanglingSymlinks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(filepath.Join(root, "link"))
	if err != nil || got != filepath.Join(root, "missing") {
		t.Fatalf("resolve: %q %v", got, err)
	}
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(filepath.Join(root, "loop")); err == nil {
		t.Fatal("accepted link loop")
	}
	if _, err := Resolve(filepath.Join(root, "missing", "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent: %v", err)
	}
}

func TestCheckPathProtectsReservedAndMounts(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cfg := config.Config{ImagesDir: filepath.Join(home, "images")}
	for _, path := range []string{root, home, cfg.ImagesDir, filepath.Join(cfg.ImagesDir, "file")} {
		if err := CheckPath(path, home, cfg, nil); err == nil {
			t.Errorf("accepted %s", path)
		}
	}
	path := filepath.Join(home, ".agents")
	if err := CheckPath(path, home, cfg, []mountinfo.Mount{{Point: filepath.Join(path, "nested")}}); err == nil {
		t.Fatal("accepted nested mount")
	}
	if err := CheckPath(path, home, cfg, nil); err != nil {
		t.Fatal(err)
	}
}
