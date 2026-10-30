package image

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
)

func TestExternalBlockBytesAreHashedAndProbed(t *testing.T) {
	dir := t.TempDir()
	blocks := []BlockFile{
		externalBlock(t, dir, "blk.000", []byte("alpha-block-bytes")),
		externalBlock(t, dir, "blk.001", []byte("beta-block-bytes")),
	}
	whole := []byte("alpha-block-bytesbeta-block-bytes")
	sum, size := wholeFacts(whole)
	cache := t.TempDir()
	path, err := AssembleBlocks(cache, blocks, sum, size)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, path, whole)
	leaf, err := os.ReadFile(blocks[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(whole, bytes.TrimSpace(leaf)) || bytes.Equal(leaf, []byte("alpha-block-bytes")) {
		t.Fatal("position-file text was treated as the block")
	}
	target := Target{Form: PayloadBlocks, Blocks: blocks}
	if err := VerifyTarget(target); err != nil {
		t.Fatal(err)
	}
	if err := QuickCheckTarget(target); err != nil {
		t.Fatal(err)
	}

	payload := positionTarget(t, blocks[0].Path)
	body, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	body[0] ^= 0xff
	if err := os.WriteFile(payload, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AssembleBlocks(t.TempDir(), blocks, sum, size); err == nil ||
		!strings.Contains(err.Error(), "blk.000") || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("known digest = %v", err)
	}
	if err := VerifyTarget(target); err == nil || !strings.Contains(err.Error(), "blk.000") ||
		!strings.Contains(err.Error(), "sha256") {
		t.Fatalf("verify known digest = %v", err)
	}
}

func TestProbeLogicalFormatUsesResolvedBlockWindows(t *testing.T) {
	dir := t.TempDir()
	head := bytes.Repeat([]byte{0}, 100)
	tail := make([]byte, logicalHeaderLen-len(head))
	copy(tail[1024-len(head):], []byte{0xE2, 0xE1, 0xF5, 0xE0})
	blocks := []BlockFile{
		externalBlock(t, dir, "blk.000", head),
		externalBlock(t, dir, "blk.001", tail),
	}
	got, err := ProbeLogicalFormat(blocks)
	if err != nil || got != FormatErofs {
		t.Fatalf("probe = %s, %v", got, err)
	}
	if _, err := container.DetectFile(blocks[0].Path); err == nil {
		t.Fatal("position file was detected as a container")
	}
	if err := os.Remove(positionTarget(t, blocks[1].Path)); err != nil {
		t.Fatal(err)
	}
	_, err = ProbeLogicalFormat(blocks)
	if err == nil || !strings.Contains(err.Error(), "blk.001") ||
		strings.Contains(err.Error(), "tiler-block-probe-") {
		t.Fatalf("broken probe = %v", err)
	}
}

func TestProbeLogicalFormatRecognizesSplitKolyTrailer(t *testing.T) {
	dir := t.TempDir()
	head := bytes.Repeat([]byte{'x'}, logicalHeaderLen)
	tail := bytes.Repeat([]byte{'y'}, logicalTrailerLen)
	copy(tail, []byte("koly"))
	blocks := []BlockFile{
		externalBlock(t, dir, "blk.000", head),
		externalBlock(t, dir, "blk.001", tail),
	}
	got, err := ProbeLogicalFormat(blocks)
	if err != nil || got != FormatDMG {
		t.Fatalf("probe = %s, %v", got, err)
	}
}

func TestProbeLogicalFormatOmitsDeletedProbePath(t *testing.T) {
	dir := t.TempDir()
	body := bytes.Repeat([]byte{'z'}, logicalHeaderLen+logicalTrailerLen)
	_, err := ProbeLogicalFormat([]BlockFile{
		externalBlock(t, dir, "blk.000", body),
	})
	if !errors.Is(err, container.ErrNotDetected) {
		t.Fatalf("probe = %v", err)
	}
	if strings.Contains(err.Error(), "tiler-block-probe-") {
		t.Fatalf("error names a deleted probe file: %v", err)
	}
}

func TestResolveVersionMountNamesBrokenExternalBlock(t *testing.T) {
	repo := testRepo(t)
	stage, node := stagedSplitVersion(t, "payload-text-for-split", 4)
	node.Format = Format{Type: FormatSquashfs, Compression: CompressionZstd}
	got, err := PublishFullVersion(linkImagesRoot(repo), logsFor(linkImagesRoot(repo)), repo.Name(), stage, node, false)
	if err != nil {
		t.Fatal(err)
	}
	last := got.Blocks[len(got.Blocks)-1]
	leaf, err := repo.ObjectBlockPath(got.ID, last.Name)
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external-block")
	if err := os.WriteFile(external, []byte("external-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writePathRef(external, leaf); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ImagesDir: linkImagesRoot(repo)}
	if _, err := ResolveVersionMount(cfg, repo.Name()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(external); err != nil {
		t.Fatal(err)
	}
	_, err = ResolveVersionMount(cfg, repo.Name())
	if err == nil || !strings.Contains(err.Error(), last.Name) {
		t.Fatalf("broken stat = %v", err)
	}
}

func externalBlock(t *testing.T, dir, name string, body []byte) BlockFile {
	t.Helper()
	payload := filepath.Join(dir, name+".payload")
	if err := os.WriteFile(payload, body, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(payload)
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(dir, name)
	if err := writePathRef(payload, leaf); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return BlockFile{
		Path: leaf, Name: name, Size: info.Size(),
		MtimeUnix: info.ModTime().Unix(), SHA256: hexDigest(sum),
	}
}

func positionTarget(t *testing.T, leaf string) string {
	t.Helper()
	body, err := os.ReadFile(leaf)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(body))
}

func TestQuickCheckExternalBlockNamesStaleMtime(t *testing.T) {
	dir := t.TempDir()
	block := externalBlock(t, dir, "blk.000", []byte("checked-block"))
	target := Target{Form: PayloadBlocks, Blocks: []BlockFile{block}}
	if err := QuickCheckTarget(target); err != nil {
		t.Fatal(err)
	}
	payload := positionTarget(t, block.Path)
	when := time.Unix(block.MtimeUnix, 0).Add(time.Hour)
	if err := os.Chtimes(payload, when, when); err != nil {
		t.Fatal(err)
	}
	err := QuickCheckTarget(target)
	if err == nil || !strings.Contains(err.Error(), "blk.000") || !strings.Contains(err.Error(), "mtime") {
		t.Fatalf("stale block = %v", err)
	}
}
