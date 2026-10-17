package cli

import (
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

func TestImagesListAllAndSingle(t *testing.T) {
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

	want := fmt.Sprintf("alice/app %s chain=0\nbob/tool %s chain=0\n", id2, id3)
	if out := mustList(t, lib); out != want {
		t.Fatalf("ls all:\n%s\nwant:\n%s", out, want)
	}
	want = fmt.Sprintf("alice/app %s chain=0\n  archive %s\n", id2, id1)
	if out := mustList(t, lib, "alice/app", "--archives"); out != want {
		t.Fatalf("ls single --archives:\n%s\nwant:\n%s", out, want)
	}
	if out := mustList(t, lib, "--archives"); !strings.Contains(out, "  archive "+id1) {
		t.Fatalf("ls all --archives:\n%s\nwant archive %s", out, id1)
	}
}

func TestImagesListBadNames(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	_, _, _, err := runCmd(t, append(lib, "images", "ls", "nope")...)
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
	_, _, _, err = runCmd(t, append(lib, "images", "ls", "bob/missing")...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	_, _, _, err = runCmd(t, append(lib, "images", "ls", "a", "b")...)
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

	want := fmt.Sprintf("alice/app %s chain=1\n  incr %s\n", fullID, incrID)
	if out := mustList(t, lib, "alice/app"); out != want {
		t.Fatalf("ls chain:\n%s\nwant:\n%s", out, want)
	}
	if out := mustRemove(t, lib, "alice/app"); out != "deprecated alice/app\n" {
		t.Fatalf("rm phase 1 printed %q", out)
	}
	want = fmt.Sprintf("alice/app %s chain=1 deprecated\n  incr %s\n", fullID, incrID)
	if out := mustList(t, lib, "alice/app"); out != want {
		t.Fatalf("ls deprecated:\n%s\nwant:\n%s", out, want)
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

	want := fmt.Sprintf("alice/app %s chain=0\n  hold %s\n", id2, id1)
	if out := mustList(t, lib, "alice/app", "--archives"); out != want {
		t.Fatalf("ls held:\n%s\nwant:\n%s", out, want)
	}
	if out := mustGC(t, lib); out != "" {
		t.Fatalf("gc with a referenced hold printed %q", out)
	}
	writeSession(t, sessions, "s1", "alice", "app", id1, "closed")
	want = fmt.Sprintf("archived alice/app %s\n", id1)
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
	if _, err := os.Stat(filepath.Join(sessions, "old.json")); !os.IsNotExist(err) {
		t.Fatalf("the aged record survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessions, "live.json")); err != nil {
		t.Fatalf("the active record was deleted: %v", err)
	}
	want := fmt.Sprintf("alice/app %s chain=0\n  hold %s\n", activeID, id1)
	if out := mustList(t, lib, "alice/app", "--archives"); out != want {
		t.Fatalf("ls after gc:\n%s\nwant:\n%s", out, want)
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
	if _, err := os.Stat(filepath.Join(sessions, "dev.json")); err != nil {
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
	want = fmt.Sprintf("alice/app %s chain=0\n  archive %s\n", ids[2], ids[1])
	if out := mustList(t, lib, "alice/app", "--archives"); out != want {
		t.Fatalf("ls after gc:\n%s\nwant:\n%s", out, want)
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
	target := "alice/app/archives/" + id1
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
		"plain", "a/b/c", "alice/app/archives/not-hex", "bad owner/app",
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
	membersID := writeMembersFixture(t, imagesDir, "bob/forest", []fixtureMember{
		{name: "docs", body: "docs-bytes"},
		{name: "src", body: "src-bytes-longer"},
	})

	// The form marker is appended for split actives only.
	want := fmt.Sprintf("alice/data %s chain=0 form=blocks\nbob/forest %s chain=0 form=members\n",
		blocksID, membersID)
	if out := mustList(t, lib); out != want {
		t.Fatalf("ls all:\n%s\nwant:\n%s", out, want)
	}
	// The single-name listing expands the payload manifest.
	want = fmt.Sprintf("alice/data %s chain=0 form=blocks\n  blocks 2\n", blocksID)
	if out := mustList(t, lib, "alice/data"); out != want {
		t.Fatalf("ls blocks:\n%s\nwant:\n%s", out, want)
	}
	want = fmt.Sprintf("bob/forest %s chain=0 form=members\n  member docs\n  member src\n",
		membersID)
	if out := mustList(t, lib, "bob/forest"); out != want {
		t.Fatalf("ls members:\n%s\nwant:\n%s", out, want)
	}
}
