//go:build linux

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
)

func TestImportedVersionLifecycle(t *testing.T) {
	home, work := withWorkspace(t)
	images, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	withBuildSeams(t, viewTable(images, "alice/app", merged, upper))
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	unknownSrc := filepath.Join(work, "unknown.sqsh")
	knownSrc := filepath.Join(work, "known.sqsh")
	writeImportPayload(t, unknownSrc)
	writeImportPayload(t, knownSrc)
	unknownBody, err := os.ReadFile(unknownSrc)
	if err != nil {
		t.Fatal(err)
	}
	knownBody, err := os.ReadFile(knownSrc)
	if err != nil {
		t.Fatal(err)
	}
	knownSum := fileSHA256(t, knownSrc)
	writeTestFile(t, knownSrc+".sha256", knownSum+"\n")
	importOK(t, lib, "link alice/app "+image.UnknownSHA256+"\n",
		"build", "--link", unknownSrc, "--image", "alice/app")
	importOK(t, lib, "link bob/app "+knownSum+"\n",
		"build", "--link", knownSrc, "--image", "bob/app")

	listed := mustList(t, lib)
	if !strings.Contains(listed, "alice/app") || !strings.Contains(listed, "bob/app") ||
		!strings.Contains(listed, image.UnknownSHA256[:10]) || !strings.Contains(listed, knownSum[:10]) {
		t.Fatalf("list = %q", listed)
	}
	shown := mustShow(t, lib, "alice/app:"+image.UnknownSHA256[:4])
	if !strings.Contains(shown, image.UnknownSHA256) || strings.Contains(shown, knownSum) {
		t.Fatalf("scoped zero selector = %q", shown)
	}
	if _, stderr, _, err := runCmd(t, append(lib, "images", "show", "bob/app:"+image.UnknownSHA256[:4])...); err == nil {
		t.Fatalf("zero selector crossed names\n%s", stderr)
	}
	assertVersionIndex(t, images, "alice/app", "")
	assertVersionIndex(t, images, "bob/app", knownSum)

	writeSessionRecord(t, sessionsDir(home), "dev",
		pinnedAction("alice/app", merged, session.ModeRW, image.UnknownSHA256, image.UnknownSHA256))
	status, stderr, _, err := runCmd(t, append(lib, "status")...)
	if err != nil || !strings.Contains(status, "dev ") || !strings.Contains(status, "alice/app") {
		t.Fatalf("status: %v\n%s\n%s", err, status, stderr)
	}
	_, stderr, _, err = runCmd(t, append(lib, "images", "rm", "alice/app:"+image.UnknownSHA256[:4])...)
	if err == nil || !strings.Contains(err.Error(), "dev") || !strings.Contains(err.Error(), "No image versions were removed") {
		t.Fatalf("referenced removal: %v\n%s", err, stderr)
	}
	if got, readErr := os.ReadFile(unknownSrc); readErr != nil || string(got) != string(unknownBody) {
		t.Fatalf("blocked removal changed external source: %v", readErr)
	}

	old := time.Now().UTC().Add(-90 * expireDay)
	knownRepo, err := image.OpenRepo(images, "bob/app")
	if err != nil {
		t.Fatal(err)
	}
	unknownRepo, err := image.OpenRepo(images, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	seedImageGCActivity(t, images, knownRepo, knownSum, old)
	seedImageGCActivity(t, images, unknownRepo, image.UnknownSHA256, old)
	withTerminalSeam(t, false)
	gcOut, stderr, gcErr := runCmdReader(t, failReader{errors.New("must not read")}, imageGCArgs(lib, "--yes", "--expire", "0")...)
	if gcErr != nil || !strings.Contains(gcOut, "removed bob/app:"+knownSum) || strings.Contains(gcOut, "removed alice/app:") {
		t.Fatalf("gc = %q\n%s\n%v", gcOut, stderr, gcErr)
	}
	if got, readErr := os.ReadFile(knownSrc); readErr != nil || string(got) != string(knownBody) {
		t.Fatalf("gc removed external known payload: %q %v", got, readErr)
	}

	writeTestFile(t, filepath.Join(upper, "new.txt"), "layer")
	t.Chdir(merged)
	stdout, stderr, cfg, err := runCmd(t, append(lib, "build", "--incremental")...)
	if err != nil {
		t.Fatalf("incremental: %v\n%s", err, stderr)
	}
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "incr" || fields[1] != "alice/app" {
		t.Fatalf("incremental stdout = %q", stdout)
	}
	child := fields[2]
	manifest, err := image.LoadVersionManifest(unknownRepo)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := manifest.SelectVersion("alice/app:" + child)
	if err != nil || len(stack) != 2 || stack[0].ID != image.UnknownSHA256 || stack[1].Parent != image.UnknownSHA256 {
		t.Fatalf("incremental stack = %+v, %v", stack, err)
	}
	action := onlyMountAction(t, sessionsDir(home), "dev")
	if action.FullID != image.UnknownSHA256 || action.TipID != child || action.ServedTip != image.UnknownSHA256 {
		t.Fatalf("replay pins = %+v", action)
	}
	replayImportedStack(t, cfg, action, unknownSrc)

	withMountSeams(t, nil)
	if _, stderr, err = mustClose(t, lib, "dev"); err != nil {
		t.Fatalf("close: %v\n%s", err, stderr)
	}
	if _, stderr, _, err = runCmd(t, append(lib, "session", "remove", "dev")...); err != nil {
		t.Fatalf("session remove: %v\n%s", err, stderr)
	}
	mustRemove(t, lib, "alice/app:"+image.UnknownSHA256[:4])
	if got, readErr := os.ReadFile(unknownSrc); readErr != nil || string(got) != string(unknownBody) {
		t.Fatalf("library removal changed external source: %q %v", got, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(unknownRepo.Dir, "_objects", image.UnknownSHA256)); !os.IsNotExist(statErr) {
		t.Fatalf("library link remains: %v", statErr)
	}
}

func assertVersionIndex(t *testing.T, images, name, want string) {
	t.Helper()
	repo, err := image.OpenRepo(images, name)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []string{"sha256.index", "content_sha256.index"} {
		body, readErr := os.ReadFile(filepath.Join(repo.Dir, index))
		text := string(body)
		if readErr != nil || strings.Contains(text, image.UnknownSHA256) ||
			(index == "sha256.index" && want != "" && !strings.Contains(text, want)) {
			t.Fatalf("%s %s = %q, %v", name, index, body, readErr)
		}
	}
}

func replayImportedStack(t *testing.T, cfg config.Config, action session.Action, external string) {
	t.Helper()
	want, err := filepath.EvalSymlinks(external)
	if err != nil {
		t.Fatal(err)
	}
	members, err := action.VersionStack(cfg.ImagesDir, false)
	if err != nil || len(members) != 2 || members[0].Meta.ID != image.UnknownSHA256 ||
		members[1].Meta.Parent != image.UnknownSHA256 {
		t.Fatalf("desired stack = %+v, %v", members, err)
	}
	served, err := action.VersionStack(cfg.ImagesDir, true)
	if err != nil || len(served) != 1 || served[0].Meta.ID != image.UnknownSHA256 {
		t.Fatalf("served stack = %+v, %v", served, err)
	}
	for _, enabled := range []bool{false, true} {
		cfg.CacheEnabled = enabled
		got, matErr := materializeVersionMember(cfg, action.Name, members[0])
		if matErr != nil || got != want {
			t.Fatalf("cache %v replay path = %s, %v", enabled, got, matErr)
		}
	}
	cache := filepath.Join(cfg.CacheDir, image.UnknownSHA256+image.SquashExt)
	if _, statErr := os.Stat(cache); !os.IsNotExist(statErr) {
		t.Fatalf("zero cache = %v", statErr)
	}
	engine := sessionEngine(cfg)
	engine.Health = func(string) bool { return false }
	engine.Clean = func(string) error { return nil }
	engine.Lock = func(string, time.Duration) (func(), error) { return func() {}, nil }
	err = engine.Open("dev", func(got session.Action) (session.Result, error) {
		target, tipErr := resolveStackTip(cfg, got.Name, got.FullID, got.TipID)
		if tipErr != nil || target.ID != image.UnknownSHA256 || target.Tip != action.TipID || len(target.Members) != 2 {
			t.Fatalf("replay target = %+v, %v", target, tipErr)
		}
		return session.Result{Mountpoint: got.Mountpoint}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
