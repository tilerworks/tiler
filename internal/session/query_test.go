package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryDirectorySelectsEveryRelatedSession(t *testing.T) {
	e, _ := lookupEngine(t, nil, nil)
	root := t.TempDir()
	app := filepath.Join(root, "app")
	seedMounted(t, e, "app", app)
	seedMounted(t, e, "nested", filepath.Join(app, "child"))
	seedMounted(t, e, "prefix", filepath.Join(root, "apple"))
	for _, tc := range []struct {
		path string
		want int
	}{
		{app, 2}, {root, 3}, {filepath.Join(app, "child", "deep"), 2},
		{filepath.Join(root, "unrelated"), 0}, {"", 3},
	} {
		got, err := e.Query("app", Query{Directory: tc.path})
		if err != nil || len(got.Sessions) != tc.want {
			t.Fatalf("Query(%q) = %+v, %v; want %d sessions", tc.path, got, err, tc.want)
		}
	}
}

func TestQueryResolvesSymlinkAncestorsAndRejectsFiles(t *testing.T) {
	e, _ := lookupEngine(t, nil, nil)
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	seedMounted(t, e, "dev", filepath.Join(root, "gone", "mount"))
	got, err := e.Query("", Query{Directory: filepath.Join(alias, "gone")})
	if err != nil || len(got.Sessions) != 1 {
		t.Fatalf("symlink ancestor query = %+v, %v", got, err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Query("", Query{Directory: file}); err == nil || !strings.Contains(err.Error(), "directory required") {
		t.Fatalf("file query error = %v", err)
	}
}

func TestQueryIncludesClosedMountsWithoutChangingOverview(t *testing.T) {
	e, _ := lookupEngine(t, nil, nil)
	point := filepath.Join(t.TempDir(), "gone")
	seedMounted(t, e, "old", point)
	rec, err := e.Load("old")
	if err != nil {
		t.Fatal(err)
	}
	rec.State, rec.ClosedAt = StateClosed, testNow
	rec.Actions[0].Status = StatusUnmounted
	if err := e.Save(rec); err != nil {
		t.Fatal(err)
	}
	got, err := e.Query("", Query{Directory: point})
	if err != nil || len(got.Sessions) != 1 || len(got.Sessions[0].Actions) != 1 || got.Sessions[0].Actions[0].Health != "" {
		t.Fatalf("closed query = %+v, %v", got, err)
	}
	overview, err := e.Overview("")
	if err != nil || len(overview.Sessions[0].Actions) != 0 {
		t.Fatalf("Overview changed = %+v, %v", overview, err)
	}
}
