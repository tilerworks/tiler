//go:build linux

package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIncrVersionSelectsEachNodesFormat(t *testing.T) {
	cfg, base := buildConfig(t), versionNode("base")
	repo, _ := OpenRepo(cfg.ImagesDir, "alice/app")
	if _, err := PublishFullVersion(cfg.ImagesDir, repo.Name(), writeSource(t, t.TempDir(), []byte("base")), base, false); err != nil {
		t.Fatal(err)
	}
	upper := upperDir(t)
	for _, marker := range []string{".wh.removed", ".wh..wh..opq"} {
		if err := os.WriteFile(filepath.Join(upper, marker), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i, pair := range []Format{{FormatErofs, "lz4"}, {FormatSquashfs, "gzip"}, {FormatSquashfs, "zstd"}} {
		cfg.SplitSize, cfg.ImageFormat, cfg.ImageCompression = int64(i*4), FormatErofs, "lz4"
		runner := &fakeRunner{body: []byte(pair.Type + pair.Compression)}
		req := IncrRequest{Config: cfg, Name: repo.Name(), Upper: upper, Subdir: "nested", BaseTip: base.ID, Now: fixedNow, MountSmoke: skipSmoke}
		if i == 2 {
			req.Config.ImageFormat, req.Config.ImageCompression = "", ""
		}
		if i == 1 {
			req.Format, req.Compression = pair.Type, pair.Compression
		}
		req.Run = func(name string, env []string, args ...string) error {
			src := args[0]
			if name == "mkfs.erofs" {
				src = args[len(args)-1]
			}
			assertBytes(t, filepath.Join(src, ".wh.removed"), nil)
			assertBytes(t, filepath.Join(src, ".wh..wh..opq"), nil)
			return runner.run(name, env, args...)
		}
		node, err := BuildIncrVersion(req)
		if err != nil || node.Format != pair || node.Parent != base.ID || (len(node.Blocks) > 0) != (i > 0) {
			t.Fatalf("new node: %#v, %v", node, err)
		}
		packer := map[string]string{FormatSquashfs: "mksquashfs", FormatErofs: "mkfs.erofs"}[pair.Type]
		if runner.calls[0][0] != packer || !strings.Contains(strings.Join(runner.calls[0], " "), pair.Compression) {
			t.Fatalf("wrong packer compression: %q", runner.calls)
		}
		base = node
		assertBytes(t, filepath.Join(upper, "new.txt"), []byte("new"))
	}
}

func TestBuildIncrVersionRejectsInvalidInputsAndRestoresUpper(t *testing.T) {
	for _, failure := range []string{"format", "compression", "dmg", "base", "packer", "smoke", "cache child", "cache parent"} {
		t.Run(failure, func(t *testing.T) {
			cfg, base := buildConfig(t), versionNode("base")
			repo, _ := OpenRepo(cfg.ImagesDir, "alice/app")
			if _, err := appendFullVersion(repo, base, false, hooks{}); err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{body: []byte("layer")}
			req := IncrRequest{Config: cfg, Name: repo.Name(), Upper: upperDir(t), Subdir: "nested", Run: runner.run, MountSmoke: skipSmoke}
			switch failure {
			case "format":
				req.Format = "unknown"
			case "compression":
				req.Format, req.Compression = FormatErofs, "xz"
			case "dmg":
				req.Format = FormatDMG
			case "base":
				req.BaseTip = strings.Repeat("a", 64)
			case "cache child":
				req.Config.CacheDir = filepath.Join(req.Upper, "cache")
			case "cache parent":
				req.Config.CacheDir = filepath.Dir(req.Upper)
			case "packer":
				runner.err = errors.New("packer failed")
			case "smoke":
				req.MountSmoke = func(string) error { return errors.New("smoke failed") }
			}
			before := readLeafFile(t, repo, fileMeta)
			if _, err := BuildIncrVersion(req); err == nil {
				t.Fatal("accepted failure")
			}
			if strings.HasPrefix(failure, "cache") && len(runner.calls) != 0 {
				t.Fatal("packer invoked with overlapping cache")
			}
			assertBytes(t, repo.MetaPath(), []byte(before))
			assertBytes(t, filepath.Join(req.Upper, "new.txt"), []byte("new"))
		})
	}
}
