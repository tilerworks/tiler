package cli

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/filelock"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
)

func TestPublishVersionAndRebaseExcludesReferenceMutation(t *testing.T) {
	cfg, engine := handoffTestEngine(t)
	oldID := strings.Repeat("a", 64)
	mountpoint := filepath.Join(t.TempDir(), "mount")
	if err := os.Mkdir(mountpoint, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := engine.New("build"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Append("build", session.Action{Type: session.TypeMount,
		Name: "alice/app", FullID: oldID, TipID: oldID, Mountpoint: mountpoint,
		Mode: "ro"}); err != nil {
		t.Fatal(err)
	}

	node, stage := handoffVersionNode(t, "published")
	entered, proceed := make(chan struct{}), make(chan struct{})
	competing := make(chan error, 1)
	go func() {
		<-entered
		hold, err := image.LockImage(cfg.ImagesDir, "alice/app", 0)
		hold.Release()
		competing <- err
		close(proceed)
	}()
	got, err := publishVersionAndRebase(cfg, mountpoint, "alice/app", func() (image.ImageMeta, error) {
		close(entered)
		<-proceed
		return image.PublishFullVersionLocked(cfg.ImagesDir, logsFor(cfg.ImagesDir), "alice/app", stage, node, false)
	}, func(meta image.ImageMeta) string { return meta.ID })
	if err != nil {
		t.Fatal(err)
	}
	if lockErr := <-competing; !errors.Is(lockErr, filelock.ErrHeld) {
		t.Fatalf("competing reference mutation entered handoff: %v", lockErr)
	}
	rec, err := engine.Load("build")
	if err != nil || rec.Actions[0].FullID != got.ID || rec.Actions[0].TipID != got.ID {
		t.Fatalf("rebased record = %#v, %v", rec, err)
	}
}

func TestResolveVersionAndPinRollsBackFailedCallback(t *testing.T) {
	cfg, engine := handoffTestEngine(t)
	node, stage := handoffVersionNode(t, "resolved")
	if _, err := image.PublishFullVersion(cfg.ImagesDir, logsFor(cfg.ImagesDir), "alice/app", stage, node, false); err != nil {
		t.Fatal(err)
	}

	failure := errors.New("pin failed")
	entered, proceed := make(chan struct{}), make(chan struct{})
	competing := make(chan error, 1)
	go func() {
		<-entered
		hold, err := image.LockImage(cfg.ImagesDir, "alice/app", 0)
		hold.Release()
		competing <- err
		close(proceed)
	}()
	err := resolveVersionAndPin(cfg, "mount", "alice/app", func(local *session.Engine, target image.Target) error {
		if err := local.New("mount"); err != nil {
			return err
		}
		action, err := local.Append("mount", session.Action{Type: session.TypeMount,
			Name: target.Name, FullID: target.ID, TipID: target.Tip,
			Mountpoint: filepath.Join(t.TempDir(), "mount"), Mode: "ro"})
		if err != nil {
			return err
		}
		if err := local.Writeback("mount", action.Seq, session.StatusMounted, action.Mountpoint); err != nil {
			return err
		}
		close(entered)
		<-proceed
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("pin failure = %v", err)
	}
	if lockErr := <-competing; !errors.Is(lockErr, filelock.ErrHeld) {
		t.Fatalf("competing reference mutation entered handoff: %v", lockErr)
	}
	if _, err := engine.Load("mount"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("failed pin left session record: %v", err)
	}
	refs := session.ReferenceCoordinator{Store: engine.Store, ImagesDir: cfg.ImagesDir}
	buckets, err := refs.References(session.ReferenceTarget{Name: "alice/app", Tip: node.ID})
	if err != nil || len(buckets) != 1 || len(buckets[0].Owners) != 0 {
		t.Fatalf("failed pin left reference: %#v, %v", buckets, err)
	}
}

func TestResolveValidatedVersionAndPinChecksNewLatest(t *testing.T) {
	cfg, _ := handoffTestEngine(t)
	old, oldStage := handoffVersionNode(t, "old latest")
	if _, err := image.PublishFullVersion(cfg.ImagesDir, logsFor(cfg.ImagesDir), "alice/app", oldStage, old, false); err != nil {
		t.Fatal(err)
	}
	preflight, err := image.ResolveVersionMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyTarget(preflight, true); err != nil {
		t.Fatalf("preflight validation: %v", err)
	}

	latest, latestStage := handoffVersionNode(t, "new latest")
	if _, err := image.PublishFullVersion(cfg.ImagesDir, logsFor(cfg.ImagesDir), "alice/app", latestStage, latest, false); err != nil {
		t.Fatal(err)
	}
	pinned, err := image.ResolveVersionMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pinned.Members[0].Path, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	called := false
	err = resolveValidatedVersionAndPin(cfg, "mount", "alice/app",
		mountFlags{verify: true}, nil,
		func(*session.Engine, image.Target) error {
			called = true
			return nil
		})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("validation error = %v", err)
	}
	if called {
		t.Fatal("mount callback ran for unverified pinned target")
	}
}

func TestVersionRuntimePinsBranchAcrossCloseAndReopen(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	f := withMountSeams(t, nil)

	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "base.txt"), "base")
	rootID := buildVersionImage(t, lib, "alice/app", src)
	mustSessionNew(t, lib, "dev")
	view := filepath.Join(work, "view")
	if err := os.Mkdir(view, 0o755); err != nil {
		t.Fatal(err)
	}
	mountOK(t, lib, "mounted "+view+"\n",
		"mount", "--session", "dev", "alice/app", view)

	upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	writeTestFile(t, filepath.Join(upper, "branch.txt"), "branch")
	f.table = viewTable(imagesDir, "alice/app", view, upper)
	chdir(t, view)
	stdout, stderr, _, err := runCmd(t, append(lib, "build")...)
	if err != nil {
		t.Fatalf("branch build: %v\n%s", err, stderr)
	}
	branchID := buildOutput(t, stdout)
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"),
		rootID, branchID, rootID, rootID)

	if out := mustCmd(t, append(lib, "session", "close", "dev")...); out != "closed dev\n" {
		t.Fatalf("close output = %q", out)
	}
	f.table = nil
	newSrc := filepath.Join(work, "new-root")
	writeTestFile(t, filepath.Join(newSrc, "new.txt"), "new root")
	newRootID := buildVersionImage(t, lib, "alice/app", newSrc)
	if newRootID == rootID {
		t.Fatalf("new root kept old id %s", rootID)
	}

	if out := mustCmd(t, append(lib, "session", "open", "dev")...); out != "opened dev\n" {
		t.Fatalf("open output = %q", out)
	}
	assertStack(t, onlyMountAction(t, sessionsDir(home), "dev"),
		rootID, branchID, "", "")
}

func TestRuntimeMountRejectsLegacyRepository(t *testing.T) {
	cfg, _ := handoffTestEngine(t)
	repo, err := image.OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.MetaPath(), []byte("version: '2.0'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveRuntimeMount(cfg, "alice/app"); err == nil ||
		!strings.Contains(err.Error(), "rebuild images and sessions") {
		t.Fatalf("legacy resolve = %v", err)
	}
}

func handoffTestEngine(t *testing.T) (config.Config, *session.Engine) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"), SessionsDir: filepath.Join(root, "sessions"),
		LogsDir: filepath.Join(root, "logs")}
	engine := &session.Engine{Store: session.Store{Dir: cfg.SessionsDir, LogsDir: cfg.LogsDir}, ImagesDir: cfg.ImagesDir,
		SessionsDir: cfg.SessionsDir, LockTimeout: -1, Versioned: true}
	original := sessionEngine
	sessionEngine = func(config.Config) *session.Engine { return engine }
	t.Cleanup(func() { sessionEngine = original })
	return cfg, engine
}

func handoffVersionNode(t *testing.T, body string) (image.ImageMeta, string) {
	t.Helper()
	stage := filepath.Join(t.TempDir(), image.ImageFile)
	data := []byte(body)
	if err := os.WriteFile(stage, data, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(stage)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	id := image.ID(sum)
	return image.ImageMeta{ID: id, Kind: image.KindFull, Size: info.Size(), SHA256: id,
		MtimeUnix: info.ModTime().Unix(), CreatedAt: time.Now().UTC(), Builder: "test",
		Format: image.Format{Type: image.FormatSquashfs, Compression: image.CompressionZstd}}, stage
}

// mustCmd runs one command through the production seams.
func mustCmd(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, _, err := runCmd(t, args...)
	if err != nil {
		t.Fatalf("command %v: %v\n%s", args, err, stderr)
	}
	return stdout
}
