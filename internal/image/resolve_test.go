package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
)

// mustRepo opens the alice/app repository used by most cases.
func mustRepo(t *testing.T, cfg config.Config) Repo {
	t.Helper()
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// layerHexID is the deterministic chain id seedChain uses for layer
// number i (1-based).
func layerHexID(i int) string {
	return fmt.Sprintf("%016x", i)
}

// seedChain publishes a full image plus a chain of that many layers.
// Every layer file is written, so callers remove files to simulate
// broken stacks.
func seedChain(t *testing.T, repo Repo, layers int) Meta {
	t.Helper()
	data := []byte("full-image")
	meta := metaFor(data)
	parent := meta.ID
	for i := 1; i <= layers; i++ {
		id := layerHexID(i)
		meta.Chain = append(meta.Chain, validEntry(parent, id))
		parent = id
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.ImagePath(), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Store(repo.MetaPath(), meta); err != nil {
		t.Fatal(err)
	}
	for _, entry := range meta.Chain {
		writeLayer(t, repo, entry.ID, []byte("layer-"+entry.ID))
	}
	return meta
}

// seedRetired parks a full image with one layer under kind/<id> so
// exclusion tests can prove retired files never join the stack.
func seedRetired(t *testing.T, repo Repo, kind, id string) {
	t.Helper()
	dir := filepath.Join(repo.Dir, kind, id)
	if err := os.MkdirAll(filepath.Join(dir, dirIncr), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("retired " + id)
	if err := os.WriteFile(filepath.Join(dir, fileImage), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Store(filepath.Join(dir, fileMeta), metaFor(body)); err != nil {
		t.Fatal(err)
	}
	layer := filepath.Join(dir, dirIncr, "cccccccccccccccc"+squashExt)
	if err := os.WriteFile(layer, []byte("retired layer"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertTarget(t *testing.T, got, want Target) {
	t.Helper()
	if got.Library != want.Library || got.Name != want.Name || got.Format != want.Format {
		t.Fatalf("target = %+v, want %+v", got, want)
	}
	if len(got.Members) != len(want.Members) {
		t.Fatalf("members =\n%+v\nwant\n%+v", got.Members, want.Members)
	}
	for i := range want.Members {
		if !memberEqual(got.Members[i], want.Members[i]) {
			t.Fatalf("members[%d] =\n%+v\nwant\n%+v", i, got.Members[i], want.Members[i])
		}
	}
}

func memberEqual(got, want Member) bool {
	return got.Path == want.Path && got.ID == want.ID &&
		got.Size == want.Size && got.MtimeUnix == want.MtimeUnix &&
		got.SHA256 == want.SHA256 && slices.Equal(got.Blocks, want.Blocks)
}

// fileFormCase is one file-path resolution expectation. An empty
// wantPath means the case must fail with every listed sentinel,
// substring, and absent substring.
type fileFormCase struct {
	name     string
	arg      string
	skip     bool
	wantPath string
	wantErrs []error
	wantText []string
	notText  []string
}

func TestResolveMountFileForm(t *testing.T) {
	dir := t.TempDir()
	// Underscore-prefixed names stay illegal library names, so they
	// exercise the file-path branch: Stat finds a regular file, or
	// ParseName rejects the library fallback.
	img := filepath.Join(dir, "_img.sqsh")
	if err := os.WriteFile(img, []byte("raw squashfs bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	spaced := filepath.Join(dir, "my image.sqsh")
	if err := os.WriteFile(spaced, []byte("spaced name"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "_a", "b", "c", "d", "e", "f")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "_sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	fifoOK := true
	if err := syscall.Mkfifo(filepath.Join(dir, "_fifo"), 0o644); err != nil {
		fifoOK = false
		t.Logf("mkfifo unavailable; skipping only the fifo case: %v", err)
	}
	tests := []fileFormCase{
		{
			name:     "absolute regular file",
			arg:      img,
			wantPath: img,
		},
		{
			name:     "relative regular file resolves to the same absolute path",
			arg:      "_img.sqsh",
			wantPath: img,
		},
		{
			name:     "name with whitespace is a file path",
			arg:      "my image.sqsh",
			wantPath: spaced,
		},
		{
			name:     "illegal library-shaped name resolves as a file path",
			arg:      filepath.Join("_a", "b", "c", "d", "e", "f"),
			wantPath: nested,
		},
		{
			name:     "missing file",
			arg:      "_missing.sqsh",
			wantErrs: []error{ErrNotImageFile, os.ErrNotExist},
			wantText: []string{"no such file or directory"},
			notText:  []string{"library reference"},
		},
		{
			name:     "directory is refused",
			arg:      "_sub",
			wantErrs: []error{ErrNotImageFile},
			wantText: []string{"is not a regular file"},
		},
		{
			name:     "non-regular file is refused",
			arg:      "_fifo",
			skip:     !fifoOK,
			wantErrs: []error{ErrNotImageFile},
			wantText: []string{"is not a regular file"},
		},
		{
			name:     "library-looking missing path names both forms",
			arg:      "_x/y/z",
			wantErrs: []error{ErrNotImageFile, ErrInvalidName},
			wantText: []string{"library reference", "invalid image name"},
		},
		{
			name:     "absolute missing path carries no library hint",
			arg:      filepath.Join(dir, "gone.sqsh"),
			wantErrs: []error{ErrNotImageFile},
			notText:  []string{"library reference"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skip {
				t.Skip("mkfifo unavailable")
			}
			t.Chdir(dir)
			cfg := buildConfig(t)
			got, err := ResolveMount(cfg, tc.arg)
			if tc.wantPath == "" {
				assertFileFormError(t, err, tc)
				return
			}
			if err != nil {
				t.Fatalf("ResolveMount(%q) = %v", tc.arg, err)
			}
			want := Target{Members: []Member{{Path: tc.wantPath}}}
			assertTarget(t, got, want)
		})
	}
}

// assertFileFormError checks the sentinel, the substrings, and the
// absent substrings one file-form case expects.
func assertFileFormError(t *testing.T, err error, tc fileFormCase) {
	t.Helper()
	if err == nil {
		t.Fatalf("ResolveMount(%q) = nil, want error", tc.arg)
	}
	for _, want := range tc.wantErrs {
		if !errors.Is(err, want) {
			t.Fatalf("err = %v, want wrapped %v", err, want)
		}
	}
	for _, text := range tc.wantText {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("err = %v, want text %q", err, text)
		}
	}
	for _, text := range tc.notText {
		if strings.Contains(err.Error(), text) {
			t.Fatalf("err = %v, must not mention %q", err, text)
		}
	}
}

func TestResolveMountLibraryFullOnly(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	meta := seedChain(t, repo, 0)
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	want := Target{
		Library: true,
		Name:    "alice/app",
		Format:  FormatSquashfs,
		Members: []Member{{
			Path:      repo.ImagePath(),
			Size:      meta.Size,
			MtimeUnix: meta.MtimeUnix,
			SHA256:    meta.SHA256,
		}},
	}
	assertTarget(t, got, want)
}

func TestResolveMountChainExpandsInStoredOrder(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	meta := seedChain(t, repo, 3)
	members := []Member{{
		Path:      repo.ImagePath(),
		Size:      meta.Size,
		MtimeUnix: meta.MtimeUnix,
		SHA256:    meta.SHA256,
	}}
	for _, entry := range meta.Chain {
		path, err := repo.IncrPath(entry.ID)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, Member{
			Path:      path,
			Size:      entry.Size,
			MtimeUnix: entry.MtimeUnix,
			SHA256:    entry.SHA256,
		})
	}
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	assertTarget(t, got, Target{
		Library: true, Name: "alice/app", Format: FormatSquashfs, Members: members,
	})
}

func TestResolveMountAcceptsStackAtDepthLimit(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	seedChain(t, repo, maxStackDepth-1)
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != maxStackDepth {
		t.Fatalf("members = %d, want %d", len(got.Members), maxStackDepth)
	}
}

func TestResolveMountIgnoresArchivesAndHolds(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	meta := seedChain(t, repo, 1)
	seedRetired(t, repo, dirArchives, "aaaaaaaaaaaaaaaa")
	seedRetired(t, repo, dirHold, "bbbbbbbbbbbbbbbb")
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	layerPath, err := repo.IncrPath(meta.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	want := Target{
		Library: true,
		Name:    "alice/app",
		Format:  FormatSquashfs,
		Members: []Member{
			{
				Path:      repo.ImagePath(),
				Size:      meta.Size,
				MtimeUnix: meta.MtimeUnix,
				SHA256:    meta.SHA256,
			},
			{
				Path:      layerPath,
				Size:      meta.Chain[0].Size,
				MtimeUnix: meta.Chain[0].MtimeUnix,
				SHA256:    meta.Chain[0].SHA256,
			},
		},
	}
	assertTarget(t, got, want)
	for _, member := range got.Members {
		if strings.Contains(member.Path, dirArchives) ||
			strings.Contains(member.Path, dirHold) {
			t.Fatalf("retired file joined the stack: %s", member.Path)
		}
	}
}

func TestResolveMountLibraryRefusals(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(t *testing.T) config.Config
		wantErrs []error
		wantText []string
	}{
		{
			name: "repository directory absent",
			prepare: func(t *testing.T) config.Config {
				return buildConfig(t)
			},
			wantErrs: []error{ErrUnknownImage},
			wantText: []string{"unknown image alice/app"},
		},
		{
			name: "active slot empty",
			prepare: func(t *testing.T) config.Config {
				cfg := buildConfig(t)
				repo := mustRepo(t, cfg)
				if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
					t.Fatal(err)
				}
				return cfg
			},
			wantErrs: []error{ErrUnknownImage},
			wantText: []string{"unknown image alice/app"},
		},
		{
			name: "metadata missing",
			prepare: func(t *testing.T) config.Config {
				cfg := buildConfig(t)
				repo := mustRepo(t, cfg)
				seedChain(t, repo, 0)
				if err := os.Remove(repo.MetaPath()); err != nil {
					t.Fatal(err)
				}
				return cfg
			},
			wantErrs: []error{ErrMetaMissing},
		},
		{
			name: "metadata corrupt surfaces the validation error",
			prepare: func(t *testing.T) config.Config {
				cfg := buildConfig(t)
				repo := mustRepo(t, cfg)
				seedChain(t, repo, 0)
				if err := os.WriteFile(repo.MetaPath(), []byte("kind: full\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return cfg
			},
			wantText: []string{"meta:", "builder: must be set"},
		},
		{
			name: "active image missing while metadata rests",
			prepare: func(t *testing.T) config.Config {
				cfg := buildConfig(t)
				repo := mustRepo(t, cfg)
				seedChain(t, repo, 0)
				if err := os.Remove(repo.ImagePath()); err != nil {
					t.Fatal(err)
				}
				return cfg
			},
			wantText: []string{"active image missing"},
		},
		{
			name: "chain layer file missing names the layer id",
			prepare: func(t *testing.T) config.Config {
				cfg := buildConfig(t)
				repo := mustRepo(t, cfg)
				seedChain(t, repo, 2)
				layer, err := repo.IncrPath(layerHexID(2))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(layer); err != nil {
					t.Fatal(err)
				}
				return cfg
			},
			wantErrs: []error{os.ErrNotExist},
			wantText: []string{"chain layer " + layerHexID(2)},
		},
		{
			name: "stack deeper than the limit",
			prepare: func(t *testing.T) config.Config {
				cfg := buildConfig(t)
				repo := mustRepo(t, cfg)
				seedChain(t, repo, maxStackDepth)
				return cfg
			},
			wantText: []string{"the limit is 8"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.prepare(t)
			_, err := ResolveMount(cfg, "alice/app")
			if err == nil {
				t.Fatal("want refusal, got nil error")
			}
			for _, want := range tc.wantErrs {
				if !errors.Is(err, want) {
					t.Fatalf("err = %v, want wrapped %v", err, want)
				}
			}
			for _, text := range tc.wantText {
				if !strings.Contains(err.Error(), text) {
					t.Fatalf("err = %v, want text %q", err, text)
				}
			}
		})
	}
}

func TestResolveMountPrefersExistingFileOverLibrary(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	_ = seedChain(t, repo, 0)
	work := t.TempDir()
	t.Chdir(work)
	if err := os.MkdirAll("alice", 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(work, "alice", "app")
	if err := os.WriteFile(file, []byte("raw"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatalf("existing regular file must win over the library name: %v", err)
	}
	if got.Library {
		t.Fatalf("target = %+v, want the file form", got)
	}
	assertTarget(t, got, Target{Members: []Member{{Path: file}}})
}

func TestResolveMountExistingFileWinsWithoutLibrary(t *testing.T) {
	cfg := buildConfig(t)
	work := t.TempDir()
	t.Chdir(work)
	if err := os.MkdirAll("alice", 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(work, "alice", "app")
	if err := os.WriteFile(file, []byte("raw"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatalf("legal name that is an existing file must mount as a file: %v", err)
	}
	if got.Library {
		t.Fatalf("target = %+v, want the file form", got)
	}
	assertTarget(t, got, Target{Members: []Member{{Path: file}}})
}

func TestResolveMountMissingFileUsesLibraryName(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	meta := seedChain(t, repo, 0)
	work := t.TempDir()
	t.Chdir(work)
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	assertTarget(t, got, Target{
		Library: true,
		Name:    "alice/app",
		Format:  FormatSquashfs,
		Members: []Member{{
			Path:      repo.ImagePath(),
			Size:      meta.Size,
			MtimeUnix: meta.MtimeUnix,
			SHA256:    meta.SHA256,
		}},
	})
}

func TestResolveMountDirectoryFallsThroughToLibrary(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	meta := seedChain(t, repo, 0)
	work := t.TempDir()
	t.Chdir(work)
	if err := os.MkdirAll(filepath.Join("alice", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatalf("directory must not take the file branch: %v", err)
	}
	if !got.Library {
		t.Fatalf("target = %+v, want the library form", got)
	}
	assertTarget(t, got, Target{
		Library: true,
		Name:    "alice/app",
		Format:  FormatSquashfs,
		Members: []Member{{
			Path:      repo.ImagePath(),
			Size:      meta.Size,
			MtimeUnix: meta.MtimeUnix,
			SHA256:    meta.SHA256,
		}},
	})
}

func TestResolveMountStatErrorIsNotLibrary(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	_ = seedChain(t, repo, 0)
	work := t.TempDir()
	t.Chdir(work)
	if err := os.Mkdir("alice", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod("alice", 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod("alice", 0o755)
	})
	probe := filepath.Join("alice", "app")
	if _, err := os.Stat(probe); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Skip("cannot provoke a non-NotExist Stat error on this platform")
	}
	_, err := ResolveMount(cfg, "alice/app")
	if err == nil {
		t.Fatal("want a Stat runtime error, got nil")
	}
	if errors.Is(err, ErrUnknownImage) || errors.Is(err, ErrNotImageFile) {
		t.Fatalf("err = %v, must not fall through to library or file-form sentinels", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want a non-NotExist Stat error", err)
	}
}
