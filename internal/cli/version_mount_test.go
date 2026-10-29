package cli

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
)

func TestVersionMountRequestPinsHistoryAndMemberFormats(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"), CacheDir: filepath.Join(root, "cache")}
	base := versionNode("a", image.KindFull, "", time.Now().UTC())
	child := versionNode("b", image.KindIncremental, base.ID, base.CreatedAt.Add(time.Minute))
	base.Format.Type, child.Format.Type = image.FormatSquashfs, image.FormatErofs
	writeVersionForest(t, cfg.ImagesDir, "alice/app", base, child)
	writeVersionPayload(t, cfg.ImagesDir, "alice/app", base, "base")
	external := filepath.Join(root, "registered.erofs")
	if err := os.WriteFile(external, []byte("child"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeVersionPayload(t, cfg.ImagesDir, "alice/app", child, external)
	action := versionMountAction(base.ID, child.ID)

	newRoot := versionNode("c", image.KindFull, "", child.CreatedAt.Add(time.Hour))
	branch := versionNode("d", image.KindIncremental, newRoot.ID, newRoot.CreatedAt.Add(time.Minute))
	writeVersionForest(t, cfg.ImagesDir, "alice/app", base, child, newRoot, branch)

	req, _, err := versionMountRequest(cfg, action, "/view", mountFlags{readOnly: true}, "/runtime")
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Members) != 2 || req.Members[0].Format != image.FormatSquashfs ||
		req.Members[1].Format != image.FormatErofs {
		t.Fatalf("members = %#v", req.Members)
	}
	for i, id := range []string{base.ID, child.ID} {
		if !strings.Contains(req.Members[i].FSName, filepath.Join("_objects", id)) {
			t.Fatalf("member %d identity = %#v", i, req.Members[i])
		}
	}
	if req.Members[0].Path != req.Members[0].FSName || req.Members[1].Path != external {
		t.Fatalf("registered payload paths = %#v", req.Members)
	}

	originalEngine := mountEngine
	originalTable := loadMountTable
	var replayed workspace.Request
	mountEngine = func(got workspace.Request) (*workspace.Workspace, error) {
		replayed = got
		return &workspace.Workspace{MergedDir: got.MergedDir}, nil
	}
	loadMountTable = func() ([]mountinfo.Mount, error) { return nil, nil }
	t.Cleanup(func() { mountEngine, loadMountTable = originalEngine, originalTable })
	installFoundTools(t)
	if _, err := sessionMount(cfg, "dev", io.Discard)(action); err != nil {
		t.Fatal(err)
	}
	if len(replayed.Members) != 2 ||
		!strings.Contains(replayed.Members[1].FSName, child.ID) {
		t.Fatalf("historical replay = %#v", replayed.Members)
	}
}

func TestVersionMountRequestCachesAndMaterializesEverySplitMember(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"), CacheDir: filepath.Join(root, "cache"), CacheEnabled: true}
	base := versionNode("a", image.KindFull, "", time.Now().UTC())
	child := versionNode("b", image.KindIncremental, base.ID, base.CreatedAt.Add(time.Minute))
	base.Blocks = []image.BlockEntry{{Name: "blk.000", Size: 1, SHA256: base.ID, MtimeUnix: 1}}
	child.Blocks = []image.BlockEntry{{Name: "blk.000", Size: 1, SHA256: child.ID, MtimeUnix: 1}}
	writeVersionForest(t, cfg.ImagesDir, "alice/app", base, child)

	original := assembleBlocks
	var ids []string
	assembleBlocks = func(_ string, _ []image.BlockFile, sum string, _ int64) (string, error) {
		ids = append(ids, sum)
		return filepath.Join(cfg.CacheDir, sum+image.SquashExt), nil
	}
	t.Cleanup(func() { assembleBlocks = original })
	req, _, err := versionMountRequest(cfg, versionMountAction(base.ID, child.ID),
		"/view", mountFlags{}, "/runtime")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != base.ID || ids[1] != child.ID || len(req.Members) != 2 {
		t.Fatalf("materialized = %v, members = %#v", ids, req.Members)
	}
	for i, id := range ids {
		if req.Members[i].Path != filepath.Join(cfg.CacheDir, id+image.SquashExt) ||
			!strings.HasSuffix(req.Members[i].FSName, "image.sqsh-concat-view") {
			t.Fatalf("split member %d = %#v", i, req.Members[i])
		}
	}
}

func versionMountAction(root, tip string) session.Action {
	return session.Action{Seq: 1, Type: session.TypeMount, Name: "alice/app",
		FullID: root, TipID: tip, RuntimeID: "m1", Mountpoint: "/view"}
}

func writeVersionPayload(t *testing.T, imagesDir, name string, node image.ImageMeta, body string) {
	t.Helper()
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	path, err := repo.ObjectPayloadPath(node)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVersionDMGMountNeedsOnlyHdiutil(t *testing.T) {
	cfg, _ := handoffTestEngine(t)
	node, stage := handoffVersionNode(t, "dmg payload")
	node.Format = image.Format{Type: image.FormatDMG, Compression: "ulfo"}
	if _, err := image.PublishFullVersion(cfg.ImagesDir, "alice/app", stage, node, false); err != nil {
		t.Fatal(err)
	}
	originalLook, originalFormat, originalEngine, originalTable := toolLookPath, toolFormatLookup, mountEngine, loadMountTable
	t.Cleanup(func() {
		toolLookPath, mountEngine, loadMountTable = originalLook, originalEngine, originalTable
		toolFormatLookup = originalFormat
	})
	toolFormatLookup = func(typ string) (container.Format, error) {
		if typ == image.FormatDMG {
			return container.DMG(), nil
		}
		return originalFormat(typ)
	}
	toolLookPath = func(name string) (string, error) {
		if name == "hdiutil" {
			return "/usr/bin/hdiutil", nil
		}
		return "", exec.ErrNotFound
	}
	loadMountTable = func() ([]mountinfo.Mount, error) { return nil, nil }
	called := false
	mountEngine = func(req workspace.Request) (*workspace.Workspace, error) {
		called = true
		if len(req.Members) != 1 || req.Members[0].Format != image.FormatDMG {
			t.Fatalf("members = %#v", req.Members)
		}
		return &workspace.Workspace{MergedDir: req.MergedDir}, nil
	}
	if _, err := runVersionMountEngine(cfg, "dev", versionMountAction(node.ID, node.ID),
		filepath.Join(t.TempDir(), "view"), mountFlags{}, filepath.Join(t.TempDir(), "runtime")); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("DMG mount did not reach the workspace engine")
	}
}

func TestFormatToolGateRejectsUnregisteredDMG(t *testing.T) {
	if _, err := container.Lookup(image.FormatDMG); err == nil {
		t.Skip("DMG is registered on this host")
	}
	original := toolLookPath
	toolLookPath = func(string) (string, error) {
		t.Fatal("tool lookup must not bypass native format restrictions")
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { toolLookPath = original })
	if err := gateFormatTools("mount", image.FormatDMG); !errors.Is(err, container.ErrUnknownType) {
		t.Fatalf("unsupported-host error = %v", err)
	}
}
