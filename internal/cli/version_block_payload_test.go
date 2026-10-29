package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
)

func TestVersionMountMaterializesExternalBlockBytes(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		ImagesDir: filepath.Join(root, "images"),
		CacheDir:  filepath.Join(root, "cache"),
	}
	alpha := []byte("alpha-block-bytes")
	beta := []byte("beta-block-bytes")
	whole := append(append([]byte{}, alpha...), beta...)
	sum := sha256.Sum256(whole)
	id := hex.EncodeToString(sum[:])
	created := time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)
	node := image.ImageMeta{
		ID: id, Kind: image.KindFull, SHA256: id, Size: int64(len(whole)),
		Format:    image.Format{Type: image.FormatSquashfs, Compression: "zstd"},
		MtimeUnix: created.Unix(), CreatedAt: created, Builder: "test",
		Blocks: []image.BlockEntry{
			blockEntry("blk.000", alpha),
			blockEntry("blk.001", beta),
		},
	}
	writeVersionForest(t, cfg.ImagesDir, "alice/app", node)
	repo, err := image.OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	leaves := []string{
		writeExternalBlock(t, repo, id, "blk.000", alpha),
		writeExternalBlock(t, repo, id, "blk.001", beta),
	}
	req, _, err := versionMountRequest(cfg, versionMountAction(id, id), "/view", mountFlags{}, "/runtime")
	if err != nil {
		t.Fatal(err)
	}
	mounted, err := os.ReadFile(req.Members[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mounted, whole) {
		t.Fatalf("mounted = %q", mounted)
	}
	leaf, err := os.ReadFile(leaves[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(mounted, bytes.TrimSpace(leaf)) {
		t.Fatal("position-file text was mounted")
	}
	if err := os.Remove(req.Members[0].Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(positionTarget(t, leaves[1])); err != nil {
		t.Fatal(err)
	}
	_, _, err = versionMountRequest(cfg, versionMountAction(id, id), "/view", mountFlags{}, "/runtime")
	if err == nil || !strings.Contains(err.Error(), "blk.001") {
		t.Fatalf("broken materialization = %v", err)
	}
}

func TestUnresolvedBlockIsNotMetadataFormatMismatch(t *testing.T) {
	t.Run("missing target", func(t *testing.T) {
		err := matchResolvedBlockFormat(blockMember(t, filepath.Join(t.TempDir(), "gone")), image.FormatSquashfs)
		assertBlockLocation(t, err)
	})
	t.Run("unreadable payload", func(t *testing.T) {
		payload := filepath.Join(t.TempDir(), "payload")
		if err := os.WriteFile(payload, []byte("secret"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(payload, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(payload, 0o644) })
		if opened, openErr := os.Open(payload); openErr == nil {
			_ = opened.Close()
			t.Skip("this user can read a mode 000 file")
		}
		err := matchResolvedBlockFormat(blockMember(t, payload), image.FormatSquashfs)
		assertBlockLocation(t, err)
	})
}

func blockMember(t *testing.T, payload string) image.Member {
	t.Helper()
	leaf := filepath.Join(t.TempDir(), "blk.000")
	if err := os.WriteFile(leaf, []byte(payload+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return image.Member{
		Format: image.FormatSquashfs,
		Blocks: []image.BlockFile{{Name: "blk.000", Path: leaf}},
	}
}

func assertBlockLocation(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a block location error")
	}
	msg := err.Error()
	if strings.Contains(msg, "does not match meta format") {
		t.Fatalf("location error reported as format mismatch: %v", err)
	}
	if strings.Contains(msg, "tiler-block-probe-") {
		t.Fatalf("error names a deleted probe file: %v", err)
	}
	if !strings.Contains(msg, "blk.000") {
		t.Fatalf("error = %v, want the block name", err)
	}
}

func blockEntry(name string, body []byte) image.BlockEntry {
	sum := sha256.Sum256(body)
	return image.BlockEntry{
		Name: name, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:]), MtimeUnix: 1,
	}
}

func writeExternalBlock(t *testing.T, repo image.Repo, id, name string, body []byte) string {
	t.Helper()
	leaf, err := repo.ObjectBlockPath(id, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(leaf), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(t.TempDir(), name+".payload")
	if err := os.WriteFile(payload, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leaf, []byte(payload+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return leaf
}

func positionTarget(t *testing.T, leaf string) string {
	t.Helper()
	body, err := os.ReadFile(leaf)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(body))
}
