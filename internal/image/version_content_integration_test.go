//go:build linux

package image

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/workspace"
)

func TestMixedVersionContentRoundTrip(t *testing.T) {
	for _, tool := range []string{"mkfs.erofs", "erofsfuse"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("mixed format content test needs %s: %v", tool, err)
		}
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skipf("mixed format content test needs /dev/fuse: %v", err)
	}
	help, _ := exec.Command("mkfs.erofs", "--help").CombinedOutput()
	if !container.ErofsToolSupports("lz4", container.ErofsCompressors(string(help))) {
		t.Skip("mixed format content test needs mkfs.erofs with lz4")
	}
	requireMountTestEnv(t)
	cfg := config.Defaults(t.TempDir())
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	req := FullRequest{Format: FormatSquashfs, Compression: "gzip", Builder: "test"}
	staged := filepath.Join(t.TempDir(), ImageFile)
	if err := packStagedImage(toolRunner(req), gitSource(t), staged, req.Format, req.Compression); err != nil {
		t.Fatal(err)
	}
	manifest, err := packedManifest(repo, req, staged, buildNow(req))
	if err != nil {
		t.Fatal(err)
	}
	tip, err := manifest.Full()
	if err != nil {
		t.Fatal(err)
	}
	tip.Format = manifest.Format
	tip, err = PublishFullVersion(cfg.ImagesDir, repo.Name(), staged, tip, false)
	if err != nil {
		t.Fatal(err)
	}
	view := mountVersionContent(t, cfg, repo, tip.ID)
	for i, format := range []Format{{FormatErofs, "lz4"}, {FormatSquashfs, "zstd"}} {
		writeMixedVersionChanges(t, view.MergedDir, i)
		if i == 0 {
			assertUpperMarkers(t, view.UpperDir)
			cfg.SplitSize = 4096
		} else {
			cfg.SplitSize = 0
		}
		want := treeDigest(t, view.MergedDir)
		next, err := BuildIncrVersion(IncrRequest{
			Config: cfg, Name: repo.Name(), Upper: view.UpperDir, BaseTip: tip.ID,
			Format: format.Type, Compression: format.Compression,
		})
		if err != nil {
			t.Fatal(err)
		}
		if next.Parent != tip.ID || next.Format != format || (len(next.Blocks) != 0) != (i == 0) {
			t.Fatalf("wrong version format or ancestry: %+v", next)
		}
		if err := view.Unmount(); err != nil {
			t.Fatal(err)
		}
		view = mountVersionContent(t, cfg, repo, next.ID)
		if got := treeDigest(t, view.MergedDir); got != want {
			t.Fatalf("mixed layer %d changed visible content: got %s, want %s", i, got, want)
		}
		entries, err := os.ReadDir(filepath.Join(view.MergedDir, "opqdir"))
		if err != nil || len(entries) != 1 || entries[0].Name() != "fresh.txt" {
			t.Fatalf("opaque directory leaked lower entries: %v, %v", entries, err)
		}
		tip = next
	}
}

func writeMixedVersionChanges(t *testing.T, root string, layer int) {
	t.Helper()
	removed := "drop.txt"
	if layer > 0 {
		removed = fmt.Sprintf("new%d.txt", layer-1)
	}
	if err := os.Remove(filepath.Join(root, removed)); err != nil {
		t.Fatal(err)
	}
	opaque := filepath.Join(root, "opqdir")
	if err := os.RemoveAll(opaque); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(opaque, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"keep.txt", fmt.Sprintf("new%d.txt", layer), "opqdir/fresh.txt"} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(fmt.Sprintf("layer %d\n", layer)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Split members use the same verified materialization path as ordinary mounts.
func mountVersionContent(t *testing.T, cfg config.Config, repo Repo, tip string) *workspace.Workspace {
	t.Helper()
	manifest, _, err := loadVersionMeta(repo)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := manifest.SelectVersion(repo.Name() + ":" + tip)
	if err != nil {
		t.Fatal(err)
	}
	var members []workspace.MountMember
	for _, node := range stack {
		path, err := repo.ObjectPayloadPath(node)
		if err != nil {
			t.Fatal(err)
		}
		if len(node.Blocks) != 0 {
			dir, err := repo.ObjectDirPath(node.ID)
			if err != nil {
				t.Fatal(err)
			}
			path, err = assembleIncrBlocks(cfg.CacheDir, dir, node)
			if err != nil {
				t.Fatal(err)
			}
		}
		members = append(members, workspace.MountMember{Path: path, Format: node.Format.Type})
	}
	view, err := workspace.Mount(workspace.Request{
		Members: members, MergedDir: filepath.Join(t.TempDir(), "view"),
		RuntimeDir: filepath.Join(cfg.SessionsDir, "test", "runtime", fmt.Sprintf("m%d", len(stack))),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := view.Unmount(); err != nil {
			t.Errorf("unmounting mixed version view: %v", err)
		}
	})
	return view
}
