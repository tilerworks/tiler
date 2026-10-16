package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/image"
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

// writeSession drops one session record that pins a full image.
func writeSession(t *testing.T, dir, name, owner, repo, fullID, state string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("owner: %s\nrepo: %s\nfull_id: %s\nstate: %s\n",
		owner, repo, fullID, state)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
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
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id1 := buildImage(t, lib, "alice/app", src)
	writeSession(t, sessions, "s1.yaml", "alice", "app", id1, "running")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v2")
	id2 := buildImage(t, lib, "alice/app", src)

	want := fmt.Sprintf("alice/app %s chain=0\n  hold %s\n", id2, id1)
	if out := mustList(t, lib, "alice/app", "--archives"); out != want {
		t.Fatalf("ls held:\n%s\nwant:\n%s", out, want)
	}
	if out := mustGC(t, lib); out != "" {
		t.Fatalf("gc with a referenced hold printed %q", out)
	}
	writeSession(t, sessions, "s1.yaml", "alice", "app", id1, "closed")
	want = fmt.Sprintf("archived alice/app %s\n", id1)
	if out := mustGC(t, lib); out != want {
		t.Fatalf("gc promotion:\n%s\nwant:\n%s", out, want)
	}
	if out := mustGC(t, lib); out != "" {
		t.Fatalf("idle gc printed %q", out)
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
	writeSession(t, sessions, "s1.yaml", "alice", "app", id1, "running")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v2")
	buildImage(t, lib, "alice/app", src)

	_, stderr, _, err := runCmd(t, append(lib, "images", "rm", "alice/app")...)
	if ExitCode(err) != 1 || !strings.Contains(stderr, "referenced") {
		t.Fatalf("ExitCode(%v) = %d stderr = %q", err, ExitCode(err), stderr)
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
	writeSession(t, sessions, "s1.yaml", "alice", "app", id1, "closed")
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
