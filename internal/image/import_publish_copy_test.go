package image

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublishCopiedImportPublishesFileAndNoopsLocal(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("vendor-payload"))
	when := time.Unix(50, 0)
	if err := os.Chtimes(src, when, when); err != nil {
		t.Fatal(err)
	}
	sidecar := strings.Repeat("ab", 32)
	if err := os.WriteFile(src+".sha256", []byte(sidecar+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := importStamp(t, nas)
	req := ImportRequest{Mode: ImportCopy, ImagesDir: images, Name: "vendor/app", Source: src}
	got := mustPublishCopy(t, req)
	sum := fileDigest(t, src)
	if len(got.Items) != 1 || got.Items[0].Action != MergeAdd || got.Items[0].Node.ID != sum || got.Items[0].Node.ID == sidecar {
		t.Fatalf("publish = %+v", got.Items)
	}
	if got.Items[0].Node.MtimeUnix != when.Unix() || got.Items[0].Node.Builder != importBuilder {
		t.Fatalf("node = %+v", got.Items[0].Node)
	}
	repo, err := OpenRepo(images, "vendor/app")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := repo.ObjectPayloadPath(got.Items[0].Node)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := ResolvePayloadBytesPath(leaf); err != nil || resolved != leaf {
		t.Fatalf("payload = %s, %v", resolved, err)
	}
	if err := QuickCheck(got.Items[0].Node, leaf); err != nil {
		t.Fatal(err)
	}
	if err := VerifySHA256(got.Items[0].Node, leaf); err != nil {
		t.Fatal(err)
	}
	meta := mustRead(t, repo.MetaPath())
	info, err := os.Stat(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	again := mustPublishCopy(t, req)
	if again.Items[0].Action != MergeNoop || again.Items[0].Node.ID != sum {
		t.Fatalf("noop = %+v", again.Items)
	}
	againInfo, err := os.Stat(repo.MetaPath())
	if err != nil || !bytes.Equal(mustRead(t, repo.MetaPath()), meta) || !againInfo.ModTime().Equal(info.ModTime()) {
		t.Fatal("local copy no-op rewrote metadata")
	}
	if importStamp(t, nas) != before {
		t.Fatal("file source changed")
	}
	if _, err := publishCopied(ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: src}, copyPublishSeam{}); err == nil {
		t.Fatal("link mode published a copy")
	}
}

func TestPublishCopiedImportPromotesSingleFile(t *testing.T) {
	withLinuxLinkPolicy(t)
	prev := linkNow
	linkNow = func() time.Time { return time.Unix(80, 0).UTC() }
	t.Cleanup(func() { linkNow = prev })
	images := t.TempDir()
	dir, node := writeCopiedFull(t, []byte("single-payload"))
	before := importStamp(t, dir)
	linkReq := ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: dir}
	linked := mustPublishLink(t, linkReq)
	if linked.Items[0].Action != MergeAdd || linked.Items[0].Node.ID != node.ID {
		t.Fatalf("link = %+v", linked.Items)
	}
	repo, err := OpenRepo(images, "vendor/app")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := repo.ObjectPayloadPath(node)
	if err != nil {
		t.Fatal(err)
	}
	pinPath := filepath.Join(t.TempDir(), "sessions", "work", "record.json")
	pin := []byte(`{"full_id":"` + node.ID + `","tip_id":"` + node.ID + `"}` + "\n")
	if err := os.MkdirAll(filepath.Dir(pinPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pinPath, pin, 0o644); err != nil {
		t.Fatal(err)
	}
	meta := mustRead(t, repo.MetaPath())
	promoted := mustPublishCopy(t, ImportRequest{Mode: ImportCopy, ImagesDir: images, Name: "vendor/app", Source: dir})
	if promoted.Items[0].Action != MergePromote || promoted.Items[0].Node.ID != node.ID ||
		promoted.Items[0].Node.MtimeUnix != node.MtimeUnix || !promoted.Items[0].Node.CreatedAt.Equal(node.CreatedAt) ||
		promoted.Items[0].Node.Builder != node.Builder || promoted.Items[0].Node.Description != node.Description {
		t.Fatalf("promote = %+v", promoted.Items[0].Node)
	}
	if !bytes.Equal(mustRead(t, repo.MetaPath()), meta) || !bytes.Equal(mustRead(t, pinPath), pin) {
		t.Fatal("promotion rewrote metadata or the pinned session identity")
	}
	if resolved, err := ResolvePayloadBytesPath(leaf); err != nil || resolved != leaf {
		t.Fatalf("promoted payload = %s, %v", resolved, err)
	}
	if err := QuickCheck(promoted.Items[0].Node, leaf); err != nil {
		t.Fatal(err)
	}
	if err := VerifySHA256(promoted.Items[0].Node, leaf); err != nil {
		t.Fatal(err)
	}
	if importStamp(t, dir) != before {
		t.Fatal("promotion changed the source")
	}
	again := mustPublishCopy(t, ImportRequest{Mode: ImportCopy, ImagesDir: images, Name: "vendor/app", Source: dir})
	if again.Items[0].Action != MergeNoop || !bytes.Equal(mustRead(t, repo.MetaPath()), meta) {
		t.Fatalf("second copy = %s", again.Items[0].Action)
	}
	source := filepath.Join(dir, dirObjects, node.ID, ImageFile)
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	cached, sum, err := EnsureCached(cache, leaf, node.ID)
	if err != nil || sum != node.ID || cached == leaf {
		t.Fatalf("materialize %s %s %v", cached, sum, err)
	}
	if !bytes.Equal(mustRead(t, cached), mustRead(t, leaf)) {
		t.Fatal("materialized bytes differ")
	}
}

func TestPublishCopiedImportPromotesSplit(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	dir, node := writeSplitLibrary(t, splitStageBodies(), "")
	before := importStamp(t, dir)
	req := ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: dir, Hash: importHashAll}
	linked := mustPublishLink(t, req)
	if len(linked.Items) != 1 || linked.Items[0].Node.ID != node.ID {
		t.Fatalf("link = %+v", linked.Items)
	}
	repo, err := OpenRepo(images, "vendor/app")
	if err != nil {
		t.Fatal(err)
	}
	meta := mustRead(t, repo.MetaPath())
	copyReq := req
	copyReq.Mode = ImportCopy
	promoted := mustPublishCopy(t, copyReq)
	if promoted.Items[0].Action != MergePromote || promoted.Items[0].Node.ID != node.ID ||
		promoted.Items[0].Node.MtimeUnix != node.MtimeUnix || len(promoted.Items[0].Node.Blocks) != len(node.Blocks) {
		t.Fatalf("promote = %+v", promoted.Items[0].Node)
	}
	for i, block := range node.Blocks {
		if promoted.Items[0].Node.Blocks[i].MtimeUnix != block.MtimeUnix || promoted.Items[0].Node.Blocks[i].SHA256 != block.SHA256 {
			t.Fatalf("block = %+v", promoted.Items[0].Node.Blocks[i])
		}
		path, err := repo.ObjectBlockPath(node.ID, block.Name)
		if err != nil {
			t.Fatal(err)
		}
		if resolved, err := ResolvePayloadBytesPath(path); err != nil || resolved != path {
			t.Fatalf("block %s = %s, %v", block.Name, resolved, err)
		}
		if err := quickCheckFile(path, block.Size, block.MtimeUnix); err != nil {
			t.Fatal(err)
		}
		if err := verifyFileSHA256(path, block.SHA256); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(mustRead(t, repo.MetaPath()), meta) {
		t.Fatal("split promotion rewrote metadata")
	}
	concat := filepath.Join(repo.Dir, dirObjects, node.ID, fileConcatView)
	if string(mustRead(t, concat)) != "blk.000\nblk.001\n" {
		t.Fatalf("concat = %q", mustRead(t, concat))
	}
	if importStamp(t, dir) != before {
		t.Fatal("split promotion changed the source")
	}
	for _, block := range node.Blocks {
		if err := os.Remove(filepath.Join(dir, dirObjects, node.ID, block.Name)); err != nil {
			t.Fatal(err)
		}
		path, err := repo.ObjectBlockPath(node.ID, block.Name)
		if err != nil {
			t.Fatal(err)
		}
		cached, sum, err := EnsureCached(t.TempDir(), path, block.SHA256)
		if err != nil || sum != block.SHA256 || !bytes.Equal(mustRead(t, cached), mustRead(t, path)) {
			t.Fatalf("materialize %s: %s %v", block.Name, sum, err)
		}
	}
}

func TestPublishCopiedImportRollsBackFailedReplacement(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	dir, node := writeSplitLibrary(t, splitStageBodies(), "")
	before := importStamp(t, dir)
	linkReq := ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: dir, Hash: importHashAll}
	if got := mustPublishLink(t, linkReq); got.Items[0].Node.ID != node.ID {
		t.Fatal(got.Items[0].Node.ID)
	}
	repo, err := OpenRepo(images, "vendor/app")
	if err != nil {
		t.Fatal(err)
	}
	meta := mustRead(t, repo.MetaPath())
	copyReq := linkReq
	copyReq.Mode = ImportCopy
	if _, err := publishCopied(copyReq, copyPublishSeam{failLeaf: 2}); err == nil || !strings.Contains(err.Error(), "injected replacement") {
		t.Fatal(err)
	}
	expectCopyLinksRestored(t, repo, node, dir)
	if !bytes.Equal(mustRead(t, repo.MetaPath()), meta) {
		t.Fatal("rollback rewrote metadata")
	}
	if importStamp(t, dir) != before {
		t.Fatal("rollback changed the source")
	}
	crashed := t.TempDir()
	crashReq := ImportRequest{Mode: ImportLink, ImagesDir: crashed, Name: "vendor/app", Source: dir, Hash: importHashAll}
	if got := mustPublishLink(t, crashReq); got.Items[0].Node.ID != node.ID {
		t.Fatal(got.Items[0].Node.ID)
	}
	crashRepo, err := OpenRepo(crashed, "vendor/app")
	if err != nil {
		t.Fatal(err)
	}
	crashReq.Mode = ImportCopy
	if _, err := publishCopied(crashReq, copyPublishSeam{halt: "objects"}); !errors.Is(err, errLinkHalted) {
		t.Fatal(err)
	}
	if err := RecoverImportTxn(crashed); err != nil {
		t.Fatal(err)
	}
	expectCopyLinksRestored(t, crashRepo, node, dir)
	if importStamp(t, dir) != before {
		t.Fatal("recovery changed the source")
	}
}

func TestPublishCopiedImportRejectsSourceConflict(t *testing.T) {
	withLinuxLinkPolicy(t)
	t.Run("file", func(t *testing.T) {
		images := t.TempDir()
		left, node := writeCopiedFull(t, []byte("same-bytes"))
		right, other := writeCopiedFull(t, []byte("same-bytes"))
		if node.ID != other.ID {
			t.Fatal("fixture digests differ")
		}
		before := importStamp(t, left) + importStamp(t, right)
		if got := mustPublishLink(t, ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: left}); got.Items[0].Node.ID != node.ID {
			t.Fatal(got.Items[0].Node.ID)
		}
		_, err := PublishCopiedImport(ImportRequest{Mode: ImportCopy, ImagesDir: images, Name: "vendor/app", Source: right})
		if err == nil || !strings.Contains(err.Error(), "source conflict") {
			t.Fatal(err)
		}
		repo, openErr := OpenRepo(images, "vendor/app")
		if openErr != nil {
			t.Fatal(openErr)
		}
		expectCopyLinksRestored(t, repo, node, left)
		if importStamp(t, left)+importStamp(t, right) != before {
			t.Fatal("conflict changed a source")
		}
	})
	t.Run("split", func(t *testing.T) {
		images := t.TempDir()
		left, node := writeSplitLibrary(t, splitStageBodies(), "")
		right, other := writeSplitLibrary(t, splitStageBodies(), "")
		if node.ID != other.ID {
			t.Fatal("fixture digests differ")
		}
		before := importStamp(t, left) + importStamp(t, right)
		req := ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: left, Hash: importHashAll}
		if got := mustPublishLink(t, req); got.Items[0].Node.ID != node.ID {
			t.Fatal(got.Items[0].Node.ID)
		}
		req.Mode = ImportCopy
		req.Source = right
		_, err := PublishCopiedImport(req)
		if err == nil || !strings.Contains(err.Error(), "source conflict") {
			t.Fatal(err)
		}
		repo, openErr := OpenRepo(images, "vendor/app")
		if openErr != nil {
			t.Fatal(openErr)
		}
		expectCopyLinksRestored(t, repo, node, left)
		if importStamp(t, left)+importStamp(t, right) != before {
			t.Fatal("conflict changed a source")
		}
	})
}

func mustPublishCopy(t *testing.T, req ImportRequest) CopyPublish {
	t.Helper()
	got, err := PublishCopiedImport(req)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func expectCopyLinksRestored(t *testing.T, repo Repo, node ImageMeta, source string) {
	t.Helper()
	images := repo.Dir
	for range repo.Segments {
		images = filepath.Dir(images)
	}
	if _, err := os.Stat(filepath.Join(images, importTxnJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, importStageDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, importBackupRel)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(node.Blocks) == 0 {
		leaf, err := repo.ObjectPayloadPath(node)
		if err != nil {
			t.Fatal(err)
		}
		want, err := canonicalPath(filepath.Join(source, dirObjects, node.ID, ImageFile))
		if err != nil || strings.TrimSpace(string(mustRead(t, leaf))) != want {
			t.Fatalf("position %q want %s, %v", mustRead(t, leaf), want, err)
		}
		return
	}
	for _, block := range node.Blocks {
		leaf, err := repo.ObjectBlockPath(node.ID, block.Name)
		if err != nil {
			t.Fatal(err)
		}
		want, err := canonicalPath(filepath.Join(source, dirObjects, node.ID, block.Name))
		if err != nil || strings.TrimSpace(string(mustRead(t, leaf))) != want {
			t.Fatalf("block %s position %q want %s, %v", block.Name, mustRead(t, leaf), want, err)
		}
	}
}

func writeCopiedFull(t *testing.T, payload []byte) (string, ImageMeta) {
	t.Helper()
	body := append([]byte("hsqs"), payload...)
	sum := sha256.Sum256(body)
	when := time.Unix(1_700_000_000, 0)
	node := fullNode(hex.EncodeToString(sum[:]), int64(len(body)), when.Unix())
	node.Builder = "vendor"
	node.Description = "Vendor base"
	node.CreatedAt = time.Unix(10, 0).UTC()
	dir := filepath.Join(t.TempDir(), "lib")
	path := filepath.Join(dir, dirObjects, node.ID, ImageFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	node.Size = info.Size()
	node.MtimeUnix = info.ModTime().Unix()
	encoded, err := EncodeVersionManifest(VersionManifest{
		Version: VersionManifestVersion, Name: "other/lib", Images: []ImageMeta{node},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileMeta), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, node
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	sum, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return hexDigest(sum)
}
