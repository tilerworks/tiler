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
		release, err := image.LockLibrary(cfg.ImagesDir, 0)
		if release != nil {
			release()
		}
		competing <- err
		close(proceed)
	}()
	got, err := publishVersionAndRebase(cfg, mountpoint, "alice/app", func() (image.ImageMeta, error) {
		close(entered)
		<-proceed
		return image.PublishFullVersionLocked(cfg.ImagesDir, "alice/app", stage, node, false)
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
	if _, err := image.PublishFullVersion(cfg.ImagesDir, "alice/app", stage, node, false); err != nil {
		t.Fatal(err)
	}

	failure := errors.New("pin failed")
	entered, proceed := make(chan struct{}), make(chan struct{})
	competing := make(chan error, 1)
	go func() {
		<-entered
		release, err := image.LockLibrary(cfg.ImagesDir, 0)
		if release != nil {
			release()
		}
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

func handoffTestEngine(t *testing.T) (config.Config, *session.Engine) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"), SessionsDir: filepath.Join(root, "sessions")}
	engine := &session.Engine{Store: session.Store{Dir: cfg.SessionsDir}, ImagesDir: cfg.ImagesDir,
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
