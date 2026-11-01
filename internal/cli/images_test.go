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
	full := append([]string{"images", "ls", "-v"}, args...)
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

func buildImmutableImage(t *testing.T, lib []string, name, src string) string {
	t.Helper()
	return buildVersionImage(t, lib, name, src)
}

// writeSession drops one JSON session record whose mount action
// pins a full image. A "closed" state closes the record and marks
// the action unmounted; every other state keeps it active.
func writeSession(t *testing.T, dir, name, imageName, fullID, state string) {
	t.Helper()
	now := time.Now().UTC()
	action := session.Action{
		Seq:        1,
		Type:       session.TypeMount,
		Image:      imageName,
		Name:       imageName,
		FullID:     fullID,
		TipID:      fullID,
		RuntimeID:  "m1",
		Mountpoint: "/ws/" + name,
		Mode:       session.ModeRW,
		Status:     session.StatusMounted,
		Timestamp:  now,
	}
	rec := session.Record{
		Version:    session.VersionRecordVersion,
		InstanceID: strings.Repeat("a", 64),
		Name:       name,
		State:      session.StateActive,
		Actions:    []session.Action{action},
		CreatedAt:  now,
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
		Version:    session.VersionRecordVersion,
		InstanceID: strings.Repeat("a", 64),
		Name:       name,
		State:      session.StateClosed,
		CreatedAt:  closedAt.Add(-time.Hour),
		ClosedAt:   closedAt,
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

func TestImagesListEmptyLibrary(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	if out := mustList(t, lib); out != "No images found.\n" {
		t.Fatalf("empty library printed %q", out)
	}
}

func TestImagesListAllVersionsAndShowSelected(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id1 := buildImmutableImage(t, lib, "alice/app", src)
	writeTestFile(t, filepath.Join(src, "a.txt"), "v2")
	id2 := buildImmutableImage(t, lib, "alice/app", src)
	other := filepath.Join(work, "other")
	writeTestFile(t, filepath.Join(other, "b.txt"), "b")
	id3 := buildImmutableImage(t, lib, "bob/tool", other)

	out := mustList(t, lib)
	for _, want := range []string{"alice/app", "latest: " + listID(id2), listID(id1),
		"bob/tool", "latest: " + listID(id3)} {
		if !strings.Contains(out, want) {
			t.Fatalf("ls all missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, id2) || strings.Contains(out, id3) {
		t.Fatalf("short list printed a full id:\n%s", out)
	}
	detail := mustShow(t, lib, "alice/app:"+id1[:12])
	for _, want := range []string{"alice/app", "  id          " + id1,
		"  parent      -", "  payload     single"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("show missing %q:\n%s", want, detail)
		}
	}
}

func TestImagesListPrefixLocatesImage(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id := buildImmutableImage(t, lib, "alice/app", src)

	short := listID(id)
	if len(short) != listIDHex || !strings.HasPrefix(id, short) {
		t.Fatalf("list id %q is not a %d-hex prefix of %s", short, listIDHex, id)
	}
	out := mustList(t, lib)
	if !strings.Contains(out, short) || strings.Contains(out, id) {
		t.Fatalf("ls id column:\n%s", out)
	}
	detail := mustShow(t, lib, "alice/app:"+short)
	if !strings.Contains(detail, "  id          "+id) {
		t.Fatalf("show by scoped prefix:\n%s", detail)
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
	_, _, _, err := runCmd(t, append(lib, "images", "show", "_foo")...)
	if ExitCode(err) != 2 {
		t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
	}
	_, _, _, err = runCmd(t, append(lib, "images", "show", "nope")...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
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
	_, stderr, _, err := runCmd(t, append(lib, "images", "show", "ffffffffffffffffffff")...)
	if ExitCode(err) != 2 || !strings.Contains(stderr, "invalid image name") {
		t.Fatalf("ExitCode(%v) = %d stderr = %q", err, ExitCode(err), stderr)
	}
}

func TestImagesShowByScopedID(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id := buildImmutableImage(t, lib, "alice/app", src)
	other := filepath.Join(work, "other")
	writeTestFile(t, filepath.Join(other, "b.txt"), "b")
	buildImmutableImage(t, lib, "bob/tool", other)

	wantID := "  id          " + id
	for _, ref := range []string{"alice/app:" + id, "alice/app:" + listID(id),
		"alice/app:" + id[:16]} {
		out := mustShow(t, lib, ref)
		if !strings.Contains(out, "alice/app") || !strings.Contains(out, wantID) {
			t.Fatalf("show %s missing alice/app or id:\n%s", ref, out)
		}
	}
}

func TestImagesListKeepsIndependentFullVersionHistory(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "base.txt"), "base")
	fullID := buildImmutableImage(t, lib, "alice/app", src)
	writeTestFile(t, filepath.Join(src, "base.txt"), "replacement")
	newID := buildImmutableImage(t, lib, "alice/app", src)
	out := mustList(t, lib)
	for _, id := range []string{fullID, newID} {
		if !strings.Contains(out, listID(id)) {
			t.Fatalf("list missing version %s:\n%s", id, out)
		}
	}
}

func TestImagesGCPreservesReferencedImmutableVersions(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessions := sessionsDir(home)
	withBuildSeams(t, nil)
	withSessionSeams(t, healthySessions)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id1 := buildImmutableImage(t, lib, "alice/app", src)
	writeSession(t, sessions, "s1", "alice/app", id1, "running")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v2")
	id2 := buildImmutableImage(t, lib, "alice/app", src)

	out := mustList(t, lib)
	if !strings.Contains(out, listID(id1)) || !strings.Contains(out, listID(id2)) {
		t.Fatalf("list omitted immutable versions:\n%s", out)
	}
	if out := mustGC(t, lib); !strings.Contains(out, "No image objects eligible") {
		t.Fatalf("gc with a referenced version printed %q", out)
	}
	writeSession(t, sessions, "s1", "alice/app", id1, "closed")
	if out := mustGC(t, lib); !strings.Contains(out, "No image objects eligible") {
		t.Fatalf("idle gc printed %q", out)
	}
	out = mustList(t, lib)
	if !strings.Contains(out, listID(id1)) || !strings.Contains(out, listID(id2)) {
		t.Fatalf("gc discarded immutable history:\n%s", out)
	}
}

func TestImagesGCPreservesExpiredSessionRecords(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessions := sessionsDir(home)
	withBuildSeams(t, nil)
	withSessionSeams(t, healthySessions)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id1 := buildImmutableImage(t, lib, "alice/app", src)
	writeSession(t, sessions, "live", "alice/app", id1, "running")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v2")
	activeID := buildImmutableImage(t, lib, "alice/app", src)
	// Image GC leaves both aged and active session records untouched.
	writeClosedSession(t, sessions, "old", time.Now().Add(-31*24*time.Hour))

	if out := mustGC(t, lib, "--yes"); strings.Contains(out, "removed") {
		t.Fatalf("gc reported a deletion:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(sessions, "old", "session.json")); err != nil {
		t.Fatalf("the aged record was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessions, "live", "session.json")); err != nil {
		t.Fatalf("the active record was deleted: %v", err)
	}
	out := mustList(t, lib)
	if !strings.Contains(out, listID(activeID)) || !strings.Contains(out, listID(id1)) {
		t.Fatalf("list after gc omitted version history:\n%s", out)
	}
}

func TestImagesGCDoesNotClassifyActiveSessions(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessions := sessionsDir(home)
	withBuildSeams(t, nil)
	// Session health and stale classification are outside image GC.
	withSessionSeams(t, func(engine *session.Engine) {
		engine.Health = func(string) bool { return false }
	})
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id1 := buildImmutableImage(t, lib, "alice/app", src)
	writeSession(t, sessions, "dev", "alice/app", id1, "running")

	if out := mustGC(t, lib); strings.Contains(out, "session dev") {
		t.Fatalf("image gc classified a session:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(sessions, "dev", "session.json")); err != nil {
		t.Fatalf("the stale active record was deleted: %v", err)
	}
}

func TestImagesGCIgnoresLegacyVersionRetention(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	var ids []string
	for _, body := range []string{"v1", "v2", "v3"} {
		writeTestFile(t, filepath.Join(src, "a.txt"), body)
		ids = append(ids, buildImmutableImage(t, lib, "alice/app", src))
	}
	if out := mustGC(t, lib, "--keep-versions=1"); !strings.Contains(out, "No image objects eligible") {
		t.Fatalf("gc removed immutable versions: %q", out)
	}
	if out := mustGC(t, lib, "--keep-versions=1"); !strings.Contains(out, "No image objects eligible") {
		t.Fatalf("second gc printed %q", out)
	}
	out := mustList(t, lib)
	for _, id := range ids {
		if !strings.Contains(out, listID(id)) {
			t.Fatalf("list after gc omitted %s:\n%s", id, out)
		}
	}
}

func TestImagesRemoveRepositoryInOnePhase(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	buildImmutableImage(t, lib, "alice/app", src)

	if out := mustRemove(t, lib, "alice/app"); out !=
		"Removed image repository alice/app (1 image versions).\n" {
		t.Fatalf("remove printed %q", out)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, "alice")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner directory survived: %v", err)
	}
	_, _, _, err := runCmd(t, append(lib, "images", "rm", "alice/app")...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
}

// TestImagesOneAndThreeSegmentFlows covers immutable publish, list,
// show, and whole-repository removal for varied name depths.
func TestImagesOneAndThreeSegmentFlows(t *testing.T) {
	for _, name := range []string{"solo", "a/b/c"} {
		t.Run(name, func(t *testing.T) {
			home, work := withWorkspace(t)
			imagesDir, lib := libFlags(home)
			withBuildSeams(t, nil)
			src := filepath.Join(work, "src")
			writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
			id1 := buildImmutableImage(t, lib, name, src)

			repo, err := image.OpenRepo(imagesDir, name)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := image.LoadVersionManifest(repo)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Name != name || len(manifest.Images) != 1 || manifest.Images[0].ID != id1 {
				t.Fatalf("meta name=%q id=%s, want %s %s",
					manifest.Name, manifest.Images[0].ID, name, id1)
			}
			object, err := repo.ObjectDirPath(id1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(object, image.ImageFile)); err != nil {
				t.Fatal(err)
			}
			out := mustList(t, lib)
			if !strings.Contains(out, name) || !strings.Contains(out, listID(id1)) {
				t.Fatalf("ls missing %s %s:\n%s", name, listID(id1), out)
			}
			if strings.Contains(out, id1) {
				t.Fatalf("ls printed the full id:\n%s", out)
			}
			if detail := mustShow(t, lib, name+":"+id1[:12]); !strings.Contains(detail, "  id          "+id1) {
				t.Fatalf("show missing id:\n%s", detail)
			}

			writeTestFile(t, filepath.Join(src, "a.txt"), "v2")
			id2 := buildImmutableImage(t, lib, name, src)
			out = mustList(t, lib)
			if !strings.Contains(out, listID(id1)) || !strings.Contains(out, listID(id2)) {
				t.Fatalf("list omitted version history:\n%s", out)
			}

			want := fmt.Sprintf("Removed image repository %s (2 image versions).\n", name)
			if out := mustRemove(t, lib, name); out != want {
				t.Fatalf("remove printed %q, want %q", out, want)
			}
			if _, err := os.Stat(imagesDir); err != nil {
				t.Fatalf("library root deleted: %v", err)
			}
			if _, err := os.Stat(repo.Dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("repo dir survived: %v", err)
			}
			if name == "a/b/c" {
				if _, err := os.Stat(filepath.Join(imagesDir, "a")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("empty namespace left behind: %v", err)
				}
			}
		})
	}
}

func TestImagesRemoveRefusesWhileReferenced(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessions := sessionsDir(home)
	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v1")
	id1 := buildImmutableImage(t, lib, "alice/app", src)
	writeSession(t, sessions, "s1", "alice/app", id1, "running")
	writeTestFile(t, filepath.Join(src, "a.txt"), "v2")
	buildImmutableImage(t, lib, "alice/app", src)

	_, stderr, _, err := runCmd(t, append(lib, "images", "rm", "alice/app")...)
	if ExitCode(err) != 1 || !strings.Contains(stderr, "referenced") {
		t.Fatalf("ExitCode(%v) = %d stderr = %q", err, ExitCode(err), stderr)
	}
	// The refusal names the authoritative pinning session.
	for _, want := range []string{"s1", listID(id1), "active"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr %q missing %q", stderr, want)
		}
	}
	repo, err := image.OpenRepo(imagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 2 {
		t.Fatalf("blocked removal changed metadata: %v %#v", err, manifest.Images)
	}

	// Once the authoritative session record is removed, the selected
	// immutable root can be removed while the other root remains.
	if err := os.RemoveAll(filepath.Join(sessions, "s1")); err != nil {
		t.Fatal(err)
	}
	target := "alice/app:" + id1[:12]
	if out := mustRemove(t, lib, target); out !=
		"Removed 1 image versions from alice/app:\n  "+listID(id1)+"\n" {
		t.Fatalf("subtree removal printed %q", out)
	}
	manifest, err = image.LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 1 || manifest.Images[0].ID == id1 {
		t.Fatalf("wrong version survived: %v %#v", err, manifest.Images)
	}
}

func TestImagesRemoveBadTargets(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withBuildSeams(t, nil)
	for _, target := range []string{
		"_foo", ".bar", "a/b/c/d/e/f", "bad owner/app",
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
	t.Skip("isolated legacy split-layout parser fixture")
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	blocksID := writeBlocksFixture(t, imagesDir, "alice/data",
		[]string{"block-one", "b2"}, true)

	out := mustList(t, lib)
	for _, want := range []string{listID(blocksID), "blocks"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ls all missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, blocksID) {
		t.Fatalf("ls printed the full id:\n%s", out)
	}
	out = mustShow(t, lib, "alice/data")
	for _, want := range []string{"  form        blocks", "  blocks      2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show blocks missing %q:\n%s", want, out)
		}
	}
}
