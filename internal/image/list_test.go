package image

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestListReposEnumeratesValidNames(t *testing.T) {
	cfg := buildConfig(t)
	for _, name := range []string{"alice/app", "alice/tool", "bob/app", "solo", "a/b/c"} {
		repo, err := OpenRepo(cfg.ImagesDir, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Empty dirs are namespaces; plant a leaf marker so ListRepos
		// records them.
		if err := os.WriteFile(filepath.Join(repo.Dir, fileMeta), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Namespace under alice with no leaf marker must not list.
	if err := os.MkdirAll(filepath.Join(cfg.ImagesDir, "alice", "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.ImagesDir, "stray dir", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ImagesDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	repos, err := ListRepos(cfg.ImagesDir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, repo := range repos {
		got = append(got, repo.Name())
	}
	slices.Sort(got)
	want := []string{"a/b/c", "alice/app", "alice/tool", "bob/app", "solo"}
	if !slices.Equal(got, want) {
		t.Fatalf("repos = %v, want %v", got, want)
	}
}

func TestListReposMissingLibrary(t *testing.T) {
	repos, err := ListRepos(filepath.Join(t.TempDir(), "absent"))
	if err != nil || len(repos) != 0 {
		t.Fatalf("repos = %v err = %v", repos, err)
	}
}

func TestInspectReportsActiveArchivesAndHolds(t *testing.T) {
	_, repo := gcRepoFixture(t)
	old := holdViaPublish(t, repo)
	archive := makeArchive(t, repo, []byte("listed-archive"), time.Now())
	state, err := Inspect(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !state.HasActive {
		t.Fatal("active image not reported")
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if state.Active.ID != loaded.ID {
		t.Fatalf("active = %+v, want id %s", state.Active, loaded.ID)
	}
	if len(state.Archives) != 1 || state.Archives[0] != archive.ID {
		t.Fatalf("archives = %v, want [%s]", state.Archives, archive.ID)
	}
	if len(state.Holds) != 1 || state.Holds[0] != old.ID {
		t.Fatalf("holds = %v, want [%s]", state.Holds, old.ID)
	}
}

func TestInspectSkipsForeignArchiveEntries(t *testing.T) {
	_, repo := gcRepoFixture(t)
	seedActive(t, repo, []byte("listed-full"), false)
	archives := filepath.Join(repo.Dir, dirArchives)
	if err := os.MkdirAll(filepath.Join(archives, "0123456789abcdef.replacing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(archives, "not-an-id"), 0o755); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Archives) != 0 {
		t.Fatalf("archives = %v, want none", state.Archives)
	}
}

func TestInspectEmptyRepository(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/absent")
	if err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(repo)
	if err != nil {
		t.Fatal(err)
	}
	if state.HasActive || len(state.Archives) != 0 || len(state.Holds) != 0 {
		t.Fatalf("state = %+v, want empty", state)
	}
}
