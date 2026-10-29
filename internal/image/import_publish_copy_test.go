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

func TestPublishCopiedImportRekeysSameSourceZeroFile(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("unknown-payload"))
	when := time.Unix(50, 0)
	if err := os.Chtimes(src, when, when); err != nil {
		t.Fatal(err)
	}
	before := importStamp(t, nas)
	req := ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: src}
	linked := mustPublishLink(t, req)
	if linked.Items[0].Action != MergeAdd || linked.Items[0].Node.ID != UnknownSHA256 {
		t.Fatalf("link = %+v", linked.Items)
	}
	repo, manifest := mustRepoManifest(t, images, "vendor/app")
	zero := closureNodeByID(manifest.Images, UnknownSHA256)
	childID := strings.Repeat("ab", 32)
	manifest.Images = append(manifest.Images, ImageMeta{
		ID: childID, SHA256: childID, Kind: KindIncremental, Parent: UnknownSHA256,
		Format: Format{Type: FormatSquashfs, Compression: CompressionZstd}, Size: 4,
		MtimeUnix: 1, CreatedAt: time.Unix(5, 0).UTC(), Description: "descendant",
	})
	body, err := EncodeVersionManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(repo.MetaPath(), body, 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repo.Dir, dirObjects, childID, "marker")
	if err = os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	pinPath, pin := pinImportSession(t, UnknownSHA256, childID)
	req.Mode = ImportCopy
	copied := mustPublishCopy(t, req)
	actual := fileDigest(t, src)
	if copied.Items[0].Action != MergeAdd || copied.Items[0].Node.ID != actual || IsUnknownSHA256(copied.Items[0].Node.ID) {
		t.Fatalf("copy = %+v", copied.Items)
	}
	repo, manifest = mustRepoManifest(t, images, "vendor/app")
	kept := closureNodeByID(manifest.Images, UnknownSHA256)
	if kept.ID != UnknownSHA256 || !kept.CreatedAt.Equal(zero.CreatedAt) || kept.Builder != zero.Builder || kept.Parent != "" {
		t.Fatalf("zero node = %+v", kept)
	}
	if closureNodeByID(manifest.Images, childID).Parent != UnknownSHA256 {
		t.Fatal("copy rewrote the descendant parent")
	}
	requirePosition(t, repo, kept, src)
	if string(mustRead(t, marker)) != "keep" || !bytes.Equal(mustRead(t, pinPath), pin) {
		t.Fatal("copy deleted a descendant or rewrote the session")
	}
	meta := mustRead(t, repo.MetaPath())
	info, err := os.Stat(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	again := mustPublishCopy(t, req)
	againInfo, err := os.Stat(repo.MetaPath())
	if err != nil || again.Items[0].Action != MergeNoop || again.Items[0].Node.ID != actual ||
		!bytes.Equal(mustRead(t, repo.MetaPath()), meta) || !againInfo.ModTime().Equal(info.ModTime()) {
		t.Fatalf("repeat = %+v", again.Items)
	}
	other := writeFakeSquashfs(t, t.TempDir(), []byte("different-source"))
	if err = os.Chtimes(other, when, when); err != nil {
		t.Fatal(err)
	}
	second := mustPublishCopy(t, ImportRequest{Mode: ImportCopy, ImagesDir: images, Name: "vendor/app", Source: other})
	secondSum := fileDigest(t, other)
	if second.Items[0].Action != MergeAdd || second.Items[0].Node.ID != secondSum || secondSum == actual {
		t.Fatalf("other = %+v", second.Items)
	}
	repo, manifest = mustRepoManifest(t, images, "vendor/app")
	if closureNodeByID(manifest.Images, UnknownSHA256).ID != UnknownSHA256 ||
		closureNodeByID(manifest.Images, actual).ID != actual ||
		closureNodeByID(manifest.Images, secondSum).ID != secondSum ||
		closureNodeByID(manifest.Images, childID).Parent != UnknownSHA256 {
		t.Fatal("different zero sources were merged or a reference was dropped")
	}
	requirePosition(t, repo, closureNodeByID(manifest.Images, UnknownSHA256), src)
	if importStamp(t, nas) != before || !bytes.Equal(mustRead(t, pinPath), pin) || string(mustRead(t, marker)) != "keep" {
		t.Fatal("copy changed the source, session, or descendant")
	}
}

func TestPublishCopiedImportRekeysDirectoryZeroRoot(t *testing.T) {
	withLinuxLinkPolicy(t)
	src := writeUnknownClosure(t)
	before := importStamp(t, src.dir) + importStamp(t, src.nas)
	modes := []struct {
		name, hash string
		ids        []string
	}{
		{name: "latest", ids: []string{src.actual, src.child.ID}},
		{name: "root prefix", hash: UnknownSHA256[:4], ids: []string{src.actual}},
		{name: "child prefix", hash: src.child.ID[:8], ids: []string{src.actual, src.child.ID}},
		{name: "other prefix", hash: src.other.ID[:8], ids: []string{src.other.ID}},
		{name: "all", hash: importHashAll, ids: []string{src.actual, src.child.ID, src.other.ID}},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			images := t.TempDir()
			got := mustPublishCopy(t, src.req(images, mode.hash, ImportCopy))
			requirePublishIDs(t, got.Items, mode.ids...)
			requireRekeyedFacts(t, got, src)
			if closureNodeByID(loadRepoManifest(t, images, "vendor/app").Images, UnknownSHA256).ID != "" {
				t.Fatal("fresh copy published the unknown id")
			}
			if mode.hash != importHashAll {
				return
			}
			repo, err := OpenRepo(images, "vendor/app")
			if err != nil {
				t.Fatal(err)
			}
			meta := mustRead(t, repo.MetaPath())
			info, err := os.Stat(repo.MetaPath())
			if err != nil {
				t.Fatal(err)
			}
			again := mustPublishCopy(t, src.req(images, importHashAll, ImportCopy))
			againInfo, err := os.Stat(repo.MetaPath())
			if err != nil || !bytes.Equal(mustRead(t, repo.MetaPath()), meta) || !againInfo.ModTime().Equal(info.ModTime()) {
				t.Fatal("repeated copy rewrote metadata")
			}
			for _, item := range again.Items {
				if item.Action != MergeNoop {
					t.Fatalf("repeat action = %s", item.Action)
				}
			}
		})
	}
	t.Run("link keeps zero", func(t *testing.T) {
		got := mustPublishLink(t, src.req(t.TempDir(), importHashAll, ImportLink))
		requirePublishIDs(t, got.Items, UnknownSHA256, src.child.ID, src.other.ID)
		if got.Items[1].Node.Parent != UnknownSHA256 {
			t.Fatalf("link parent = %s", got.Items[1].Node.Parent)
		}
	})
	t.Run("existing child parent conflicts", func(t *testing.T) {
		images := t.TempDir()
		linked := mustPublishLink(t, src.req(images, importHashAll, ImportLink))
		if linked.Items[0].Node.ID != UnknownSHA256 || linked.Items[1].Node.Parent != UnknownSHA256 {
			t.Fatalf("link = %+v", linked.Items)
		}
		repo, err := OpenRepo(images, "vendor/app")
		if err != nil {
			t.Fatal(err)
		}
		meta := mustRead(t, repo.MetaPath())
		pinPath, pin := pinImportSession(t, UnknownSHA256, src.child.ID)
		_, err = PublishCopiedImport(src.req(images, importHashAll, ImportCopy))
		if err == nil || !strings.Contains(err.Error(), "identity conflict") {
			t.Fatal(err)
		}
		if !bytes.Equal(mustRead(t, repo.MetaPath()), meta) || !bytes.Equal(mustRead(t, pinPath), pin) {
			t.Fatal("conflict rewrote metadata or the session")
		}
		if _, statErr := os.Stat(filepath.Join(repo.Dir, dirObjects, src.actual)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatal(statErr)
		}
		manifest := loadRepoManifest(t, images, "vendor/app")
		if closureNodeByID(manifest.Images, src.child.ID).Parent != UnknownSHA256 {
			t.Fatal("conflict rewrote the existing child")
		}
		requirePosition(t, repo, closureNodeByID(manifest.Images, UnknownSHA256), src.external)
	})
	t.Run("root copy keeps descendants and session", func(t *testing.T) {
		images := t.TempDir()
		if got := mustPublishLink(t, src.req(images, importHashAll, ImportLink)); got.Items[0].Node.ID != UnknownSHA256 {
			t.Fatal(got.Items[0].Node.ID)
		}
		pinPath, pin := pinImportSession(t, UnknownSHA256, src.child.ID)
		got := mustPublishCopy(t, src.req(images, UnknownSHA256[:4], ImportCopy))
		requirePublishIDs(t, got.Items, src.actual)
		if got.Items[0].Action != MergeAdd || got.Items[0].Node.Description != "unknown link" {
			t.Fatalf("root = %+v", got.Items[0].Node)
		}
		repo, manifest := mustRepoManifest(t, images, "vendor/app")
		child := closureNodeByID(manifest.Images, src.child.ID)
		if child.Parent != UnknownSHA256 || closureNodeByID(manifest.Images, UnknownSHA256).ID != UnknownSHA256 ||
			closureNodeByID(manifest.Images, src.other.ID).ID != src.other.ID {
			t.Fatal("root copy deleted or reparented a committed node")
		}
		stack, err := manifest.SelectVersion(manifest.Name + ":" + src.child.ID)
		if err != nil || len(stack) != 2 || stack[0].ID != UnknownSHA256 || stack[1].ID != src.child.ID {
			t.Fatalf("session stack = %+v %v", stack, err)
		}
		requirePosition(t, repo, closureNodeByID(manifest.Images, UnknownSHA256), src.external)
		if !bytes.Equal(mustRead(t, pinPath), pin) {
			t.Fatal("root copy rewrote the session")
		}
		index := string(mustRead(t, filepath.Join(repo.Dir, fileSHA256Index)))
		if strings.Contains(index, UnknownSHA256) || !strings.Contains(index, src.actual) {
			t.Fatalf("index = %s", index)
		}
		meta := mustRead(t, repo.MetaPath())
		again := mustPublishCopy(t, src.req(images, UnknownSHA256[:4], ImportCopy))
		if again.Items[0].Action != MergeNoop || !bytes.Equal(mustRead(t, repo.MetaPath()), meta) || !bytes.Equal(mustRead(t, pinPath), pin) {
			t.Fatal("repeated root copy changed committed references")
		}
	})
	if importStamp(t, src.dir)+importStamp(t, src.nas) != before {
		t.Fatal("directory copy changed the source")
	}
}

type unknownClosure struct {
	dir, nas, external, actual string
	zero, child, other         ImageMeta
}

func (u unknownClosure) req(images, hash string, mode ImportMode) ImportRequest {
	return ImportRequest{Mode: mode, ImagesDir: images, Name: "vendor/app", Source: u.dir, Hash: hash}
}

func writeUnknownClosure(t *testing.T) unknownClosure {
	t.Helper()
	nas := t.TempDir()
	external := writeFakeSquashfs(t, nas, []byte("unknown-root"))
	when := time.Unix(50, 0)
	if err := os.Chtimes(external, when, when); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(external)
	if err != nil {
		t.Fatal(err)
	}
	zero := fullNode(UnknownSHA256, info.Size(), info.ModTime().Unix())
	zero.Description = "unknown link"
	zero.Builder = "vendor"
	zero.CreatedAt = time.Unix(10, 0).UTC()
	dir := filepath.Join(t.TempDir(), "lib")
	writeClosureLink(t, dir, zero.ID, external)
	child := writeDigestedNode(t, dir, []byte("child-payload"), time.Unix(60, 0), 30)
	child.Kind = KindIncremental
	child.Parent = UnknownSHA256
	child.Builder = ""
	child.Description = "child"
	other := writeDigestedNode(t, dir, []byte("other-root"), time.Unix(70, 0), 20)
	other.Description = "other"
	if strings.HasPrefix(child.ID, "0000") || strings.HasPrefix(other.ID, "0000") || child.ID[:8] == other.ID[:8] {
		t.Fatalf("fixture prefixes collide: %s %s", child.ID, other.ID)
	}
	encoded, err := EncodeVersionManifest(VersionManifest{
		Version: VersionManifestVersion, Name: "other/lib", Images: []ImageMeta{zero, child, other},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, fileMeta), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	return unknownClosure{
		dir: dir, nas: nas, external: external, actual: fileDigest(t, external),
		zero: zero, child: child, other: other,
	}
}

func writeClosureLink(t *testing.T, dir, id, src string) {
	t.Helper()
	path := filepath.Join(dir, dirObjects, id, ImageFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePathRef(src, path); err != nil {
		t.Fatal(err)
	}
}

func writeDigestedNode(t *testing.T, dir string, payload []byte, when time.Time, created int64) ImageMeta {
	t.Helper()
	body := append([]byte("hsqs"), payload...)
	sum := sha256.Sum256(body)
	id := hex.EncodeToString(sum[:])
	path := filepath.Join(dir, dirObjects, id, ImageFile)
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
	node := fullNode(id, info.Size(), info.ModTime().Unix())
	node.CreatedAt = time.Unix(created, 0).UTC()
	node.Builder = "vendor"
	return node
}

func requirePublishIDs(t *testing.T, items []ImportMergeItem, ids ...string) {
	t.Helper()
	if len(items) != len(ids) {
		t.Fatalf("ids = %d, want %d", len(items), len(ids))
	}
	for i, id := range ids {
		if items[i].Node.ID != id {
			t.Fatalf("item %d = %s, want %s", i, items[i].Node.ID, id)
		}
	}
}

func requireRekeyedFacts(t *testing.T, got CopyPublish, src unknownClosure) {
	t.Helper()
	for _, item := range got.Items {
		if item.Action != MergeAdd {
			t.Fatalf("action = %s", item.Action)
		}
		switch item.Node.ID {
		case src.actual:
			kept := item.Node.Parent == "" && item.Node.Description == "unknown link" && item.Node.Builder == "vendor" &&
				item.Node.CreatedAt.Equal(src.zero.CreatedAt) && !IsUnknownSHA256(item.Node.SHA256)
			if !kept {
				t.Fatalf("root = %+v", item.Node)
			}
		case src.child.ID:
			if item.Node.Parent != src.actual || item.Node.Description != "child" {
				t.Fatalf("child = %+v", item.Node)
			}
		case src.other.ID:
			if item.Node.Parent != "" || item.Node.Description != "other" {
				t.Fatalf("other = %+v", item.Node)
			}
		default:
			t.Fatalf("unexpected %s", item.Node.ID)
		}
	}
}

func pinImportSession(t *testing.T, fullID, tipID string) (string, []byte) {
	t.Helper()
	body := []byte(`{"full_id":"` + fullID + `","tip_id":"` + tipID + `"}` + "\n")
	path := filepath.Join(t.TempDir(), "sessions", "work", "record.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, body
}

func mustRepoManifest(t *testing.T, images, name string) (Repo, VersionManifest) {
	t.Helper()
	repo, err := OpenRepo(images, name)
	if err != nil {
		t.Fatal(err)
	}
	return repo, loadRepoManifest(t, images, name)
}

func loadRepoManifest(t *testing.T, images, name string) VersionManifest {
	t.Helper()
	repo, err := OpenRepo(images, name)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func requirePosition(t *testing.T, repo Repo, node ImageMeta, loc string) {
	t.Helper()
	want, err := canonicalPath(loc)
	got := strings.TrimSpace(payloadText(t, repo, node))
	if err != nil || got != want {
		t.Fatalf("position %q want %s, %v", got, want, err)
	}
}
