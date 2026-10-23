package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
)

func mustList(t *testing.T, lib []string, args ...string) string {
	t.Helper()
	full := append([]string{"images", "ls"}, args...)
	stdout, stderr, _, err := runCmd(t, append(lib, full...)...)
	if err != nil {
		t.Fatalf("images ls %v: %v\n%s", args, err, stderr)
	}
	return stdout
}

func mustShow(t *testing.T, lib []string, args ...string) string {
	t.Helper()
	full := append([]string{"images", "show"}, args...)
	stdout, stderr, _, err := runCmd(t, append(lib, full...)...)
	if err != nil {
		t.Fatalf("images show %v: %v\n%s", args, err, stderr)
	}
	return stdout
}

func mustGC(t *testing.T, lib []string, args ...string) string {
	t.Helper()
	full := append([]string{"images", "gc"}, args...)
	stdout, stderr, _, err := runCmd(t, append(lib, full...)...)
	if err != nil {
		t.Fatalf("images gc %v: %v\n%s", args, err, stderr)
	}
	return stdout
}

func mustRemove(t *testing.T, lib []string, target string) string {
	t.Helper()
	stdout, stderr, _, err := runCmd(t, append(lib, "images", "rm", target)...)
	if err != nil {
		t.Fatalf("images rm %s: %v\n%s", target, err, stderr)
	}
	return stdout
}

// writeSession drops one JSON session record whose mount action
// pins a full image. A "closed" state closes the record and marks
// the action unmounted; every other state keeps it active.
func writeSession(t *testing.T, dir, name, owner, repo, fullID, state string) {
	t.Helper()
	now := time.Now().UTC()
	action := session.Action{
		Seq:        1,
		Type:       session.TypeMount,
		Image:      owner + "/" + repo,
		Owner:      owner,
		Repo:       repo,
		FullID:     fullID,
		Mountpoint: "/ws/" + name,
		Mode:       session.ModeRW,
		Status:     session.StatusMounted,
		Timestamp:  now,
	}
	rec := session.Record{
		Name:      name,
		State:     session.StateActive,
		Actions:   []session.Action{action},
		CreatedAt: now,
	}
	if state == "closed" {
		rec.State = session.StateClosed
		rec.ClosedAt = now
		rec.Actions[0].Status = session.StatusUnmounted
	}
	if err := (session.Store{Dir: dir}).Save(rec); err != nil {
		t.Fatal(err)
	}
}

// writeClosedSession drops one closed record completed at the given
// time, aged past the default session retention.
func writeClosedSession(t *testing.T, dir, name string, closedAt time.Time) {
	t.Helper()
	rec := session.Record{
		Name:      name,
		State:     session.StateClosed,
		CreatedAt: closedAt.Add(-time.Hour),
		ClosedAt:  closedAt,
	}
	if err := (session.Store{Dir: dir}).Save(rec); err != nil {
		t.Fatal(err)
	}
}

func TestFormatSizeUsesBinaryUnits(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{11, "11 B"},
		{1024, "1 KiB"},
		{1536, "1.5 KiB"},
		{2048, "2 KiB"},
		{128 << 20, "128 MiB"},
	}
	for _, tt := range cases {
		if got := formatSize(tt.n); got != tt.want {
			t.Errorf("formatSize(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestImageTableSuffixAndDetail(t *testing.T) {
	created := time.Date(2026, 10, 20, 9, 14, 3, 0, time.UTC)
	state := image.State{
		Active: image.Meta{
			ID: "0123456789abcdef", Size: 128 << 20,
			SHA256:     strings.Repeat("ab", 32),
			CreatedAt:  created,
			Builder:    "dev",
			Deprecated: true,
			Source:     image.Source{Repo: "git.example/app", Ref: "main", Commit: "abcdef1234567890"},
			Chain: []image.ChainEntry{{
				ID: "1111111111111111", Parent: "0123456789abcdef",
				Size: 12 << 20, CreatedAt: created.Add(time.Hour),
			}},
		},
		HasActive: true,
		Archives:  []string{"2222222222222222"},
	}
	var buf bytes.Buffer
	if err := writeImageTable(&buf, []namedState{{name: "alice/app", state: state}}, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "\t") || !strings.Contains(buf.String(), "deprecated ref=main commit=abcdef1") {
		t.Fatalf("short row:\n%s", buf.String())
	}
	buf.Reset()
	if err := writeImageDetail(&buf, "alice/app", state); err != nil {
		t.Fatal(err)
	}
	detail := buf.String()
	for _, want := range []string{
		"  source      repo=git.example/app ref=main commit=abcdef1234567890\n",
		"  deprecated  true\n",
		"    1111111111111111  parent=0123456789abcdef  12 MiB  2026-10-20T10:14:03Z\n",
		"  archives    1\n    2222222222222222\n",
		"  holds       0\n",
		"134217728 (128 MiB)",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail missing %q:\n%s", want, detail)
		}
	}
}

func TestImagesListEmptyLibrary(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	if out := mustList(t, lib); out != "" {
		t.Fatalf("empty library printed %q", out)
	}
	if out := mustList(t, lib, "--archives"); out != "" {
		t.Fatalf("empty library printed %q", out)
	}
}

func TestImagesListAllAndShow(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id1 := buildImage(t, lib, "alice/app", src)
	writeTestFile(t, filepath.Join(src, "a.txt"), "v2")
	id2 := buildImage(t, lib, "alice/app", src)
	other := filepath.Join(work, "other")
	writeTestFile(t, filepath.Join(other, "b.txt"), "b")
	id3 := buildImage(t, lib, "bob/tool", other)

	out := mustList(t, lib)
	for _, want := range []string{"NAME", "alice/app", id2, "bob/tool", id3} {
		if !strings.Contains(out, want) {
			t.Fatalf("ls all missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, id1) {
		t.Fatalf("short list included archived id %s:\n%s", id1, out)
	}
	detail := mustShow(t, lib, "alice/app")
	for _, want := range []string{"  id          " + id2, "    " + id1, "  holds       0"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("show missing %q:\n%s", want, detail)
		}
	}
	if out = mustList(t, lib, "--archives"); !strings.Contains(out, "  archive "+id1) {
		t.Fatalf("ls all --archives:\n%s\nwant archive %s", out, id1)
	}
}

func TestImagesListRejectsAName(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	_, _, _, err := runCmd(t, append(lib, "images", "ls", "alice/app")...)
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
}

func TestImagesShowBadNames(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	_, _, _, err := runCmd(t, append(lib, "images", "show", "nope")...)
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
	_, _, _, err = runCmd(t, append(lib, "images", "show", "bob/missing")...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	_, _, _, err = runCmd(t, append(lib, "images", "show", "a", "b")...)
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
	_, _, _, err = runCmd(t, append(lib, "images", "show")...)
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
}

func TestImagesListChainAndDeprecated(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(home, "lib", "runtime", "up-1-2", "upper")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "base.txt"), "base")
	fullID := buildImage(t, lib, "alice/app", src)
	writeTestFile(t, filepath.Join(upper, "new.txt"), "change")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(merged)
	stdout, stderr, _, err := runCmd(t, append(lib, "build")...)
	if err != nil {
		t.Fatalf("incr build: %v\n%s", err, stderr)
	}
	incrID := buildOutput(t, stdout)

	out := mustShow(t, lib, "alice/app")
	for _, want := range []string{
		"  chain       1",
		"    " + incrID + "  parent=" + fullID,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show chain missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "deprecated") {
		t.Fatalf("fresh image listed deprecated:\n%s", out)
	}
	if out := mustRemove(t, lib, "alice/app"); out != "deprecated alice/app\n" {
		t.Fatalf("rm phase 1 printed %q", out)
	}
	out = mustShow(t, lib, "alice/app")
	if !strings.Contains(out, "  deprecated  true") {
		t.Fatalf("show deprecated:\n%s", out)
	}
}

func TestImagesGCPromotesHeldChain(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessions := sessionsDir(home)
	withBuildSeams(t, nil)
	withSessionSeams(t, healthySessions)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id1 := buildImage(t, lib, "alice/app", src)
	writeSession(t, sessions, "s1", "alice", "app", id1, "running")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v2")
	id2 := buildImage(t, lib, "alice/app", src)

	out := mustShow(t, lib, "alice/app")
	if !strings.Contains(out, id2) || !strings.Contains(out, "    "+id1) || !strings.Contains(out, "  holds       1") {
		t.Fatalf("show held:\n%s\nwant active %s and hold %s", out, id2, id1)
	}
	if out := mustGC(t, lib); out != "" {
		t.Fatalf("gc with a referenced hold printed %q", out)
	}
	writeSession(t, sessions, "s1", "alice", "app", id1, "closed")
	want := fmt.Sprintf("archived alice/app %s\n", id1)
	if out := mustGC(t, lib); out != want {
		t.Fatalf("gc promotion:\n%s\nwant:\n%s", out, want)
	}
	if out := mustGC(t, lib); out != "" {
		t.Fatalf("idle gc printed %q", out)
	}
}

func TestImagesGCCleansExpiredSessionRecords(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessions := sessionsDir(home)
	withBuildSeams(t, nil)
	withSessionSeams(t, healthySessions)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id1 := buildImage(t, lib, "alice/app", src)
	// The pinning record must exist before the second build, which
	// is what retires the first chain to a hold instead of an
	// archive.
	writeSession(t, sessions, "live", "alice", "app", id1, "running")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v2")
	activeID := buildImage(t, lib, "alice/app", src)
	// An aged closed record is reclaimed; the active record both
	// survives and keeps the hold out of the archives.
	writeClosedSession(t, sessions, "old", time.Now().Add(-31*24*time.Hour))

	if out := mustGC(t, lib); out != "removed session old\n" {
		t.Fatalf("gc report:\n%s\nwant:\nremoved session old\n", out)
	}
	if _, err := os.Stat(filepath.Join(sessions, "old")); !os.IsNotExist(err) {
		t.Fatalf("the aged record survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessions, "live", "session.json")); err != nil {
		t.Fatalf("the active record was deleted: %v", err)
	}
	out := mustShow(t, lib, "alice/app")
	if !strings.Contains(out, activeID) || !strings.Contains(out, "    "+id1) || !strings.Contains(out, "  holds       1") {
		t.Fatalf("show after gc:\n%s\nwant active %s and hold %s", out, activeID, id1)
	}
}

func TestImagesGCReportsStaleActiveSessions(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessions := sessionsDir(home)
	withBuildSeams(t, nil)
	// Every recorded mount verifies dead, so the active session is
	// reported stale and never deleted.
	withSessionSeams(t, func(engine *session.Engine) {
		engine.Health = func(string) bool { return false }
	})
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id1 := buildImage(t, lib, "alice/app", src)
	writeSession(t, sessions, "dev", "alice", "app", id1, "running")

	if out := mustGC(t, lib); out != "stale session dev\n" {
		t.Fatalf("gc report:\n%s\nwant:\nstale session dev\n", out)
	}
	if _, err := os.Stat(filepath.Join(sessions, "dev", "session.json")); err != nil {
		t.Fatalf("the stale active record was deleted: %v", err)
	}
}

func TestImagesGCExpiresByCount(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	var ids []string
	for _, body := range []string{"v1", "v2", "v3"} {
		writeTestFile(t, filepath.Join(src, "a.txt"), body)
		ids = append(ids, buildImage(t, lib, "alice/app", src))
	}
	want := fmt.Sprintf("removed alice/app %s\n", ids[0])
	if out := mustGC(t, lib, "--keep-versions=1"); out != want {
		t.Fatalf("gc expire:\n%s\nwant:\n%s", out, want)
	}
	if out := mustGC(t, lib, "--keep-versions=1"); out != "" {
		t.Fatalf("second gc printed %q", out)
	}
	out := mustShow(t, lib, "alice/app")
	if !strings.Contains(out, "    "+ids[1]) || !strings.Contains(out, "  archives    1") {
		t.Fatalf("show after gc:\n%s\nwant archive %s", out, ids[1])
	}
}

func TestImagesRemoveTwoPhases(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	buildImage(t, lib, "alice/app", src)

	if out := mustRemove(t, lib, "alice/app"); out != "deprecated alice/app\n" {
		t.Fatalf("phase 1 printed %q", out)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, "alice", "app", "image.sqsh")); err != nil {
		t.Fatalf("phase 1 deleted files: %v", err)
	}
	if out := mustRemove(t, lib, "alice/app"); out != "removed alice/app\n" {
		t.Fatalf("phase 2 printed %q", out)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, "alice")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner directory survived: %v", err)
	}
	_, _, _, err := runCmd(t, append(lib, "images", "rm", "alice/app")...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
}

func TestImagesRemoveRefusesWhileReferenced(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessions := sessionsDir(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id1 := buildImage(t, lib, "alice/app", src)
	writeSession(t, sessions, "s1", "alice", "app", id1, "running")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v2")
	buildImage(t, lib, "alice/app", src)

	_, stderr, _, err := runCmd(t, append(lib, "images", "rm", "alice/app")...)
	if ExitCode(err) != 1 || !strings.Contains(stderr, "referenced") {
		t.Fatalf("ExitCode(%v) = %d stderr = %q", err, ExitCode(err), stderr)
	}
	// The refusal names the pinning session and how to release it.
	for _, want := range []string{"s1", "session close s1"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q missing %q", stderr, want)
		}
	}
	meta, err := image.Load(filepath.Join(imagesDir, "alice", "app", "meta.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Deprecated {
		t.Fatal("referenced target was deprecated")
	}

	// After the reference closes, gc promotes the hold and the
	// archive target walks both rm phases.
	writeSession(t, sessions, "s1", "alice", "app", id1, "closed")
	mustGC(t, lib)
	target := "alice/app/_archives/" + id1
	if out := mustRemove(t, lib, target); out != "deprecated "+target+"\n" {
		t.Fatalf("archive phase 1 printed %q", out)
	}
	if out := mustRemove(t, lib, target); out != "removed "+target+"\n" {
		t.Fatalf("archive phase 2 printed %q", out)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, "alice", "app", "image.sqsh")); err != nil {
		t.Fatalf("active image deleted: %v", err)
	}
}

func TestImagesRemoveBadTargets(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	for _, target := range []string{
		"plain", "a/b/c", "alice/app/archives/not-hex", "alice/app/_archives/not-hex", "bad owner/app",
	} {
		_, _, _, err := runCmd(t, append(lib, "images", "rm", target)...)
		if ExitCode(err) != 2 {
			t.Fatalf("target %q: ExitCode(%v) = %d, want 2", target, err, ExitCode(err))
		}
	}
	_, _, _, err := runCmd(t, append(lib, "images", "rm")...)
	if ExitCode(err) != 2 {
		t.Fatalf("missing argument: ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
}

func TestImagesListSplitForms(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	blocksID := writeBlocksFixture(t, imagesDir, "alice/data",
		[]string{"block-one", "b2"}, true)

	out := mustList(t, lib)
	for _, want := range []string{blocksID, "blocks"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ls all missing %s:\n%s", want, out)
		}
	}
	out = mustShow(t, lib, "alice/data")
	for _, want := range []string{"  form        blocks", "  blocks      2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show blocks missing %q:\n%s", want, out)
		}
	}
}
