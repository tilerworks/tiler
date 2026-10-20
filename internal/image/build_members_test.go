package image

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
)

func membersConfig(t *testing.T) config.Config {
	t.Helper()
	return buildConfig(t)
}

func membersRequest(cfg config.Config, src string, run ToolRunner) FullRequest {
	return FullRequest{
		Config:  cfg,
		Name:    "alice/app",
		SrcDir:  src,
		Builder: "test",
		Now:     fixedNow,
		Run:     run,
	}
}

// memberRunner packs each source directory from a digest of its
// on-disk content, so identical trees pack identically and any
// content change shows up in the member file.
type memberRunner struct {
	calls [][]string
	err   error
}

func (m *memberRunner) run(name string, _ []string, args ...string) error {
	m.calls = append(m.calls, append([]string{name}, args...))
	if m.err != nil {
		return m.err
	}
	body := []byte("packed:" + dirFingerprint(args[0]))
	return os.WriteFile(args[1], body, 0o644)
}

// dirFingerprint hashes the sorted entry names and file contents
// under dir without depending on its location.
func dirFingerprint(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "unreadable"
	}
	var text string
	for _, entry := range entries {
		body, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			body = []byte("<dir>")
		}
		text += entry.Name() + " " + hex.EncodeToString(body) + "\n"
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// memberTree lays out one data.txt per top-level directory.
func memberTree(t *testing.T, dirs map[string]string) string {
	t.Helper()
	src := t.TempDir()
	for name, content := range dirs {
		if err := os.MkdirAll(filepath.Join(src, name), 0o755); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(src, name, "data.txt")
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

func TestBuildMembersPublishesSplitPayload(t *testing.T) {
	cfg := membersConfig(t)
	src := memberTree(t, map[string]string{"web": "web-content", "api": "api-content"})
	runner := &memberRunner{}
	res, err := BuildMembers(membersRequest(cfg, src, runner.run))
	if err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != repo.MembersDirPath() {
		t.Fatalf("result path = %s", res.Path)
	}
	for _, name := range []string{"api", "web"} {
		path := filepath.Join(repo.MembersDirPath(), name+squashExt)
		want := []byte("packed:" + dirFingerprint(filepath.Join(src, name)))
		assertBytes(t, path, want)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Members) != 2 ||
		loaded.Members[0].Name != "api" || loaded.Members[1].Name != "web" {
		t.Fatalf("manifest = %+v", loaded.Members)
	}
	var total int64
	for i, entry := range loaded.Members {
		path := filepath.Join(repo.MembersDirPath(), entry.Name+squashExt)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Image != "members/"+entry.Name+".sqsh" ||
			entry.Size != int64(len(body)) ||
			entry.SHA256 != hex.EncodeToString(sum[:]) ||
			entry.MtimeUnix != info.ModTime().Unix() {
			t.Fatalf("members[%d] = %+v", i, entry)
		}
		total += entry.Size
		if entry.MtimeUnix > loaded.MtimeUnix {
			t.Fatalf("meta mtime %d is behind members[%d]", loaded.MtimeUnix, i)
		}
	}
	if loaded.SHA256 != membersDigest(loaded.Members) ||
		loaded.ID != loaded.SHA256[:idHexLen] || loaded.Size != total {
		t.Fatalf("meta identity = %+v", loaded)
	}
	if res.ID != loaded.ID || res.SHA256 != loaded.SHA256 {
		t.Fatalf("result = %+v", res)
	}
	if loaded.Kind != KindFull || len(loaded.Chain) != 0 ||
		!loaded.CreatedAt.Equal(fixedNow()) {
		t.Fatalf("meta provenance = %+v", loaded)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %v", runner.calls)
	}
	call := runner.calls[0]
	want := []string{"mksquashfs", filepath.Join(src, "api"), call[2],
		"-comp", "zstd", "-noappend"}
	if !equalArgs(call, want) {
		t.Fatalf("runner call = %q", call)
	}
	tmp := filepath.Dir(call[2])
	if filepath.Dir(tmp) != repo.Dir ||
		!strings.HasPrefix(filepath.Base(tmp), ".members.tmp.") {
		t.Fatalf("member output %s is outside the scratch payload dir", call[2])
	}
	assertNoTemps(t, repo.Dir)
}

func TestBuildMembersSortedPartitioning(t *testing.T) {
	cfg := membersConfig(t)
	src := memberTree(t, map[string]string{
		"zebra": "z", "alpha": "a", "middle": "m",
	})
	runner := &memberRunner{}
	if _, err := BuildMembers(membersRequest(cfg, src, runner.run)); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"alpha", "middle", "zebra"}
	if len(loaded.Members) != len(wantNames) {
		t.Fatalf("manifest = %+v", loaded.Members)
	}
	for i, want := range wantNames {
		if loaded.Members[i].Name != want {
			t.Fatalf("members[%d] = %s, want %s", i, loaded.Members[i].Name, want)
		}
		if got := filepath.Base(runner.calls[i][1]); got != want {
			t.Fatalf("pack order %d = %s, want %s", i, got, want)
		}
	}
}

func TestBuildMembersDerivedDigestDeterminism(t *testing.T) {
	base := map[string]string{"alpha": "a", "beta": "b"}
	first := buildMembersID(t, base)
	if again := buildMembersID(t, base); again != first {
		t.Fatalf("same set derived different ids: %s != %s", first, again)
	}
	if other := buildMembersID(t, map[string]string{
		"alpha": "a", "beta": "changed",
	}); other == first {
		t.Fatal("content change kept the derived id")
	}
	if swapped := buildMembersID(t, map[string]string{
		"alpha": "b", "beta": "a",
	}); swapped == first {
		t.Fatal("swapped member content kept the derived id")
	}
	if fewer := buildMembersID(t, map[string]string{
		"alpha": "a",
	}); fewer == first {
		t.Fatal("partition set change kept the derived id")
	}
}

func buildMembersID(t *testing.T, dirs map[string]string) string {
	t.Helper()
	cfg := membersConfig(t)
	res, err := BuildMembers(membersRequest(cfg, memberTree(t, dirs),
		(&memberRunner{}).run))
	if err != nil {
		t.Fatal(err)
	}
	return res.ID
}

func TestBuildMembersRefusesTopLevelFile(t *testing.T) {
	cfg := membersConfig(t)
	src := memberTree(t, map[string]string{"web": "w"})
	if err := os.WriteFile(filepath.Join(src, "loose.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &memberRunner{}
	_, err := BuildMembers(membersRequest(cfg, src, runner.run))
	if err == nil {
		t.Fatal("expected refusal")
	}
	text := err.Error()
	if !strings.Contains(text, "loose.txt") {
		t.Errorf("refusal does not name the entry: %s", text)
	}
	if !strings.Contains(text, "blocks") {
		t.Errorf("refusal lacks blocks guidance: %s", text)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran on a refused source: %v", runner.calls)
	}
	assertNothingPublished(t, cfg)
}

func TestBuildMembersRefusesEmptySource(t *testing.T) {
	cfg := membersConfig(t)
	runner := &memberRunner{}
	_, err := BuildMembers(membersRequest(cfg, t.TempDir(), runner.run))
	if err == nil || !strings.Contains(err.Error(), "no top-level directories") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran on an empty source: %v", runner.calls)
	}
	assertNothingPublished(t, cfg)
}

func TestBuildMembersNoOpOnIdenticalRebuild(t *testing.T) {
	cfg := membersConfig(t)
	src := memberTree(t, map[string]string{"alpha": "a", "beta": "b"})
	first, err := BuildMembers(membersRequest(cfg, src, (&memberRunner{}).run))
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildMembers(membersRequest(cfg, src, (&memberRunner{}).run))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("rebuild derived %s, want %s", second.ID, first.ID)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := loadActive(repo); err != nil || !exists {
		t.Fatalf("loadActive = %v %v", exists, err)
	}
	assertNoTemps(t, repo.Dir)
}

func TestBuildMembersCleansUpOnPackFailure(t *testing.T) {
	cfg := membersConfig(t)
	src := memberTree(t, map[string]string{"alpha": "a", "beta": "b"})
	var packed int
	packer := func(_ string, _ []string, args ...string) error {
		if packed == 0 {
			packed++
			return os.WriteFile(args[1], []byte("first-member"), 0o644)
		}
		return errors.New("mksquashfs: exit status 1: boom")
	}
	_, err := BuildMembers(membersRequest(cfg, src, packer))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(repo.MembersDirPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("members dir err = %v", statErr)
	}
	if _, statErr := os.Stat(repo.MetaPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active meta err = %v", statErr)
	}
	assertNoTemps(t, repo.Dir)
}

func TestBuildMembersGuidesOnToolSizeLimit(t *testing.T) {
	cfg := membersConfig(t)
	src := memberTree(t, map[string]string{"alpha": "a"})
	runner := &memberRunner{err: errors.New(
		"mksquashfs: exit status 1: FATAL ERROR: File too large for filesystem")}
	_, err := BuildMembers(membersRequest(cfg, src, runner.run))
	if err == nil {
		t.Fatal("expected failure")
	}
	assertSplitGuidance(t, err)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	assertNoTemps(t, repo.Dir)
}

func TestBuildMembersCleansUpOnPublishFailure(t *testing.T) {
	cfg := membersConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	// A hold whose meta cannot be parsed makes the publish
	// preparation fail after every member was already packed.
	hold, err := repo.HoldPath("0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hold, fileMeta), []byte("not: [yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo.Dir, dirIncr), 0o755); err != nil {
		t.Fatal(err)
	}
	src := memberTree(t, map[string]string{"alpha": "a"})
	_, err = BuildMembers(membersRequest(cfg, src, (&memberRunner{}).run))
	if err == nil {
		t.Fatal("expected publish failure")
	}
	if _, statErr := os.Stat(repo.MetaPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active meta err = %v", statErr)
	}
	if _, statErr := os.Stat(repo.MembersDirPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("members dir err = %v", statErr)
	}
	assertNoTemps(t, repo.Dir)
}

// assertNothingPublished checks that a refused build created no
// active slot content.
func assertNothingPublished(t *testing.T, cfg config.Config) {
	t.Helper()
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(repo.Dir); errors.Is(statErr, os.ErrNotExist) {
		return
	}
	if _, statErr := os.Stat(repo.MetaPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active meta err = %v", statErr)
	}
	assertNoTemps(t, repo.Dir)
}
