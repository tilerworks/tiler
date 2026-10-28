package session

import (
	"os"
	"path/filepath"
	"testing"
)

func symlinkDir(t *testing.T, name string) (link, resolved string) {
	t.Helper()
	base := t.TempDir()
	concrete := filepath.Join(base, "concrete")
	if err := os.Mkdir(concrete, 0o755); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(base, name)
	if err := os.Symlink(concrete, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	return link, resolved
}
