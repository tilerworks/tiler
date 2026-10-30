package image

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublishLinkedImportCommitsFilePositionsOnce(t *testing.T) {
	withLinuxLinkPolicy(t)
	prev := linkNow
	linkNow = func() time.Time { return time.Unix(100, 0).UTC() }
	t.Cleanup(func() { linkNow = prev })
	images := t.TempDir()
	nas := t.TempDir()
	src := writeFakeSquashfs(t, nas, []byte("vendor-payload"))
	when := time.Unix(50, 0)
	if err := os.Chtimes(src, when, when); err != nil {
		t.Fatal(err)
	}
	sum := strings.Repeat("ab", 32)
	if err := os.WriteFile(src+".sha256", []byte(sum+"  "+filepath.Base(src)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sealDir(t, nas)
	before := importStamp(t, nas)
	req := ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: src}
	got := mustPublishLink(t, req)
	if got.Name != "vendor/app" || len(got.Items) != 1 || got.Items[0].Action != MergeAdd || got.Items[0].Node.ID != sum {
		t.Fatalf("publish = %+v", got)
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
	leaf, err := repo.ObjectPayloadPath(got.Items[0].Node)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := canonicalPath(src)
	if err != nil || string(mustRead(t, leaf)) != loc+"\n" {
		t.Fatalf("position = %q, %v", mustRead(t, leaf), err)
	}
	st, err := os.Lstat(leaf)
	if err != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("position mode = %v, %v", st, err)
	}
	if err := QuickCheck(got.Items[0].Node, leaf); err != nil {
		t.Fatal(err)
	}
	index := string(mustRead(t, filepath.Join(repo.Dir, fileSHA256Index)))
	if !strings.Contains(index, sum) || strings.Contains(index, "stale") {
		t.Fatalf("index = %q", index)
	}
	again := mustPublishLink(t, req)
	if again.Items[0].Action != MergeNoop || !bytes.Equal(mustRead(t, repo.MetaPath()), meta) {
		t.Fatal("no-op rewrote metadata")
	}
	againInfo, err := os.Stat(repo.MetaPath())
	if err != nil || !againInfo.ModTime().Equal(info.ModTime()) {
		t.Fatalf("no-op metadata time %v, was %v", againInfo.ModTime(), info.ModTime())
	}
	if err := os.WriteFile(filepath.Join(repo.Dir, fileSHA256Index), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := writeFakeSquashfs(t, t.TempDir(), []byte("second-payload"))
	if err := os.Chtimes(other, when, when); err != nil {
		t.Fatal(err)
	}
	secondSum := strings.Repeat("cd", 32)
	if err := os.WriteFile(other+".sha256", []byte(secondSum+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := mustPublishLink(t, ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: other})
	if second.Items[0].Action != MergeAdd || second.Items[0].Node.ID != secondSum {
		t.Fatalf("second = %+v", second.Items)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 2 || manifest.Images[0].ID != sum ||
		!manifest.Images[0].CreatedAt.Equal(time.Unix(100, 0).UTC()) || manifest.Images[0].Builder != importBuilder {
		t.Fatalf("manifest = %+v, %v", manifest.Images, err)
	}
	index = string(mustRead(t, filepath.Join(repo.Dir, fileSHA256Index)))
	if strings.Contains(index, "stale") || !strings.Contains(index, sum) || !strings.Contains(index, secondSum) {
		t.Fatalf("refreshed index = %q", index)
	}
	zeroSrc := writeFakeSquashfs(t, t.TempDir(), []byte("unknown-payload"))
	if err := os.Chtimes(zeroSrc, when, when); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zeroSrc+".sha256", []byte(UnknownSHA256+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unknown := mustPublishLink(t, ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/zero", Source: zeroSrc})
	if !IsUnknownSHA256(unknown.Items[0].Node.ID) {
		t.Fatalf("unknown = %s", unknown.Items[0].Node.ID)
	}
	zeroRepo, err := OpenRepo(images, "vendor/zero")
	if err != nil {
		t.Fatal(err)
	}
	zeroIndex := string(mustRead(t, filepath.Join(zeroRepo.Dir, fileSHA256Index)))
	if strings.Contains(zeroIndex, UnknownSHA256) {
		t.Fatalf("unknown index = %q", zeroIndex)
	}
	if _, err := os.Stat(filepath.Join(zeroRepo.Dir, fileVersionRevision)); err != nil {
		t.Fatal(err)
	}
	if importStamp(t, nas) != before {
		t.Fatal("file source changed")
	}
	if _, err := publishLinked(ImportRequest{Mode: ImportCopy, ImagesDir: images, Name: "vendor/app", Source: src}, linkPublishSeam{}); err == nil {
		t.Fatal("copy mode published a link")
	}
}

func TestPublishLinkedImportKeepsDirectorySource(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	dir, parent, child := writePublishDir(t)
	sealDir(t, dir)
	before := importStamp(t, dir)
	req := ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: dir, Hash: importHashAll}
	got := mustPublishLink(t, req)
	if len(got.Items) != 2 || got.Items[0].Action != MergeAdd || got.Items[1].Action != MergeAdd {
		t.Fatalf("publish = %+v", got.Items)
	}
	repo, err := OpenRepo(images, "vendor/app")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil || manifest.Name != "vendor/app" || len(manifest.Images) != 2 ||
		manifest.Images[0].Description != "Vendor base" || !manifest.Images[0].CreatedAt.Equal(parent.CreatedAt) {
		t.Fatalf("manifest = %+v, %v", manifest, err)
	}
	leaf, err := repo.ObjectPayloadPath(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := QuickCheck(manifest.Images[0], leaf); err != nil {
		t.Fatal(err)
	}
	for _, block := range child.Blocks {
		path, err := repo.ObjectBlockPath(child.ID, block.Name)
		if err != nil {
			t.Fatal(err)
		}
		target := strings.TrimSpace(string(mustRead(t, path)))
		info, err := os.Stat(target)
		if err != nil || info.Size() != block.Size || !filepath.IsAbs(target) {
			t.Fatalf("block %s position %q, %v", block.Name, target, err)
		}
	}
	concat := filepath.Join(repo.Dir, dirObjects, child.ID, fileConcatView)
	if string(mustRead(t, concat)) != "blk.000\nblk.001\n" {
		t.Fatalf("concat = %q", mustRead(t, concat))
	}
	sourceConcat := filepath.Join(dir, dirObjects, child.ID, fileConcatView)
	if string(mustRead(t, sourceConcat)) != "old-view\n" {
		t.Fatal("source concat changed")
	}
	meta := mustRead(t, repo.MetaPath())
	if again := mustPublishLink(t, req); again.Items[0].Action != MergeNoop || again.Items[1].Action != MergeNoop ||
		!bytes.Equal(mustRead(t, repo.MetaPath()), meta) {
		t.Fatal("directory no-op rewrote metadata")
	}
	if importStamp(t, dir) != before {
		t.Fatal("directory source changed")
	}
}

func TestPublishLinkedImportRollsBackAndRecovers(t *testing.T) {
	withLinuxLinkPolicy(t)
	dir, _, child := writePublishDir(t)
	sealDir(t, dir)
	before := importStamp(t, dir)
	req := func(images string) ImportRequest {
		return ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: dir, Hash: importHashAll}
	}
	images := t.TempDir()
	if _, err := publishLinked(req(images), linkPublishSeam{failID: child.ID}); err == nil {
		t.Fatal("injected failure published")
	}
	expectLinkRolledBack(t, images, "vendor/app")
	if importStamp(t, dir) != before {
		t.Fatal("rollback changed the source")
	}

	crashed := t.TempDir()
	if _, err := publishLinked(req(crashed), linkPublishSeam{halt: "objects"}); !errors.Is(err, errLinkHalted) {
		t.Fatal(err)
	}
	if _, err := os.Stat(mustControl(t, crashed, "vendor/app", importTxnJournal)); err != nil {
		t.Fatal(err)
	}
	if err := RecoverImportTxn(crashed, "vendor/app"); err != nil {
		t.Fatal(err)
	}
	expectLinkRolledBack(t, crashed, "vendor/app")

	metaCrash := t.TempDir()
	if _, err := publishLinked(req(metaCrash), linkPublishSeam{halt: "meta"}); !errors.Is(err, errLinkHalted) {
		t.Fatal(err)
	}
	if err := RecoverImportTxn(metaCrash, "vendor/app"); err != nil {
		t.Fatal(err)
	}
	expectLinkRolledBack(t, metaCrash, "vendor/app")

	indexCrash := t.TempDir()
	if _, err := publishLinked(req(indexCrash), linkPublishSeam{halt: "index"}); !errors.Is(err, errLinkHalted) {
		t.Fatal(err)
	}
	if err := RecoverImportTxn(indexCrash, "vendor/app"); err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(indexCrash, "vendor/app")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 2 {
		t.Fatalf("committed manifest = %+v, %v", manifest.Images, err)
	}
	if _, err := os.Stat(mustControl(t, indexCrash, "vendor/app", importTxnJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, fileSHA256Index)); err != nil {
		t.Fatal(err)
	}
	if importStamp(t, dir) != before {
		t.Fatal("recovery changed the source")
	}
}

func mustPublishLink(t *testing.T, req ImportRequest) LinkPublish {
	t.Helper()
	got, err := PublishLinkedImport(req)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func expectLinkRolledBack(t *testing.T, images, name string) {
	t.Helper()
	repo, err := OpenRepo(images, name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mustControl(t, images, name, importTxnJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err == nil && len(manifest.Images) != 0 {
		t.Fatalf("rolled back manifest = %+v", manifest.Images)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	objects := filepath.Join(repo.Dir, dirObjects)
	entries, err := os.ReadDir(objects)
	if err == nil && len(entries) != 0 {
		t.Fatalf("objects remain: %d", len(entries))
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func writePublishDir(t *testing.T) (string, ImageMeta, ImageMeta) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "src")
	parent := fullNode(strings.Repeat("a", 64), 1, 1)
	parent.Description = "Vendor base"
	parent.Builder = "vendor"
	parent.CreatedAt = time.Unix(10, 0).UTC()
	body := append([]byte("hsqs"), []byte("parent")...)
	parent = writePublishPayload(t, dir, parent, body)
	child := fullNode(strings.Repeat("b", 64), 1, 1)
	child.Kind = KindIncremental
	child.Parent = parent.ID
	child.Builder = ""
	child.Description = ""
	child.CreatedAt = time.Unix(20, 0).UTC()
	blocks := [][]byte{
		append([]byte("hsqs"), []byte("zero")...),
		append([]byte("hsqs"), []byte("one")...),
	}
	var entries []BlockEntry
	var size int64
	for i, blockBody := range blocks {
		entry := BlockEntry{Name: "blk.000", SHA256: strings.Repeat("1", 64)}
		if i == 1 {
			entry.Name = "blk.001"
			entry.SHA256 = strings.Repeat("2", 64)
		}
		entry, err := writePublishBlock(t, dir, child.ID, entry, blockBody)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
		size += entry.Size
	}
	child.Blocks = entries
	child.Size = size
	nodes := []ImageMeta{parent, child}
	encoded, err := EncodeVersionManifest(VersionManifest{
		Version: VersionManifestVersion, Name: "other/lib", Images: nodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileMeta), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, dirObjects, child.ID, fileConcatView)
	if err := os.WriteFile(old, []byte("old-view\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, parent, child
}

func writePublishPayload(t *testing.T, dir string, node ImageMeta, body []byte) ImageMeta {
	t.Helper()
	path := filepath.Join(dir, dirObjects, node.ID, ImageFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	node.Size = info.Size()
	node.MtimeUnix = info.ModTime().Unix()
	return node
}

func writePublishBlock(t *testing.T, dir, id string, entry BlockEntry, body []byte) (BlockEntry, error) {
	t.Helper()
	path := filepath.Join(dir, dirObjects, id, entry.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return BlockEntry{}, err
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return BlockEntry{}, err
	}
	when := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, when, when); err != nil {
		return BlockEntry{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return BlockEntry{}, err
	}
	entry.Size = info.Size()
	entry.MtimeUnix = info.ModTime().Unix()
	return entry, nil
}
