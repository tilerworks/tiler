package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const oldSplitConcat = "/old/nas/blk.001\n/old/nas/blk.000\n"

func TestStageImportBlocksCopiesOrderedPayloadOnce(t *testing.T) {
	images := t.TempDir()
	dir, node := writeSplitLibrary(t, splitStageBodies(), "")
	payload := relocateBlock(t, dir, node, node.Blocks[1])
	stamp := importStamp(t, dir)
	var opens []string
	var reads int64
	staged := stageBlocksOK(t, copyReq(images, dir), node.ID, stageCopyHooks{open: func(path string) (io.ReadCloser, error) {
		opens = append(opens, path)
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return countingFile{t, file, &reads}, nil
	}})
	t.Cleanup(staged.Remove)
	leaf := filepath.Join(dir, dirObjects, node.ID, node.Blocks[0].Name)
	if len(opens) != 2 || opens[0] != leaf || opens[1] != payload || reads != node.Size {
		t.Fatalf("opens %v reads %d size %d", opens, reads, node.Size)
	}
	if staged.SHA256 != node.SHA256 || staged.Size != node.Size || staged.Format != node.Format {
		t.Fatalf("staged = %+v", staged)
	}
	wantConcat := node.Blocks[0].Name + "\n" + node.Blocks[1].Name + "\n"
	if string(mustRead(t, filepath.Join(staged.Dir, fileConcatView))) != wantConcat {
		t.Fatal("concat view was not rebuilt from the manifest")
	}
	if string(mustRead(t, filepath.Join(dir, dirObjects, node.ID, fileConcatView))) != oldSplitConcat {
		t.Fatal("source concat changed")
	}
	for i, block := range staged.Blocks {
		got := mustRead(t, filepath.Join(staged.Dir, block.Name))
		want := splitStageBodies()[i]
		if block.SHA256 != node.Blocks[i].SHA256 || block.Size != node.Blocks[i].Size || !bytes.Equal(got, want) {
			t.Fatalf("block %s = %+v", block.Name, block)
		}
		info, err := os.Stat(filepath.Join(staged.Dir, block.Name))
		if err != nil || info.ModTime().Unix() != node.Blocks[i].MtimeUnix {
			t.Fatalf("mtime %s err %v", block.Name, err)
		}
	}
	repo := mustStageRepo(t, images)
	assertUnpublished(t, repo)
	if importStamp(t, dir) != stamp {
		t.Fatal("source tree changed")
	}
	staged.Remove()
	if _, err := os.Stat(staged.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup left %s", staged.Dir)
	}
}

type splitFault struct {
	name, want string
	badWhole   bool
	touchMeta  bool
	cancel     bool
	setup      func(*testing.T, string, ImageMeta)
	open       func(string) (io.ReadCloser, error)
	sync       func(*os.File) error
}

func TestStageImportBlocksRejectsBrokenCopies(t *testing.T) {
	cases := []splitFault{
		{name: "missing block", want: "blk.001", setup: removeSecondBlock},
		{name: "reordered blocks", want: "blk.000", setup: swapBlockPayloads},
		{name: "corrupt block", want: "digest mismatch", setup: corruptFirstBlock},
		{name: "whole digest", want: "logical image digest", badWhole: true},
		{name: "sync", want: "sync failed", sync: func(*os.File) error { return errors.New("sync failed") }},
		{name: "source changed", want: "source changed", open: openChangingSource},
		{name: "metadata changed", want: "metadata changed", touchMeta: true},
		{name: "canceled", want: "context canceled", cancel: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			images := t.TempDir()
			whole := ""
			if tc.badWhole {
				whole = strings.Repeat("ab", 32)
			}
			dir, node := writeSplitLibrary(t, splitStageBodies(), whole)
			if tc.setup != nil {
				tc.setup(t, dir, node)
			}
			repo := mustStageRepo(t, images)
			if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo.Dir, "keep"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			ctx, seam := tc.seam(t, dir)
			_, err := stageImportBlocks(ctx, copyReq(images, dir), node.ID, seam)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			assertNoVisibleVersion(t, images)
			if _, statErr := os.Stat(filepath.Join(repo.Dir, "keep")); statErr != nil {
				t.Fatalf("existing target file: %v", statErr)
			}
		})
	}
}

func (tc splitFault) seam(t *testing.T, dir string) (context.Context, stageCopyHooks) {
	t.Helper()
	ctx := context.Background()
	seam := stageCopyHooks{open: tc.open, sync: tc.sync}
	if tc.cancel {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		seam.open = func(path string) (io.ReadCloser, error) {
			file, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			return cancelOnRead{File: file, cancel: cancel}, nil
		}
	}
	if tc.touchMeta {
		seam.open = func(path string) (io.ReadCloser, error) {
			if err := os.WriteFile(filepath.Join(dir, fileMeta), []byte("changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return os.Open(path)
		}
	}
	return ctx, seam
}

func TestStageImportBlocksRejectsUnstagedInputs(t *testing.T) {
	images := t.TempDir()
	nas := t.TempDir()
	file := writeFakeSquashfs(t, nas, []byte("plain"))
	plain := filepath.Join(nas, "lib")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	writeImportLibrary(t, plain)
	dir, node := writeSplitLibrary(t, splitStageBodies(), "")
	stamp := importStamp(t, nas)
	id := strings.Repeat("ab", 32)
	cases := []struct {
		name, source, version, want string
		mode                        ImportMode
	}{
		{name: "link", source: dir, version: node.ID, mode: ImportLink, want: "copy mode"},
		{name: "file", source: file, version: node.ID, mode: ImportCopy, want: "image directory"},
		{name: "missing version", source: dir, version: strings.Repeat("cd", 32), mode: ImportCopy, want: "not in source"},
		{name: "no blocks", source: plain, version: id, mode: ImportCopy, want: "requires blocks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := copyReq(images, tc.source)
			req.Mode = tc.mode
			_, err := StageImportBlocks(context.Background(), req, tc.version)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v", err)
			}
			repo := mustStageRepo(t, images)
			if _, statErr := os.Stat(repo.Dir); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("target created: %v", statErr)
			}
		})
	}
	if importStamp(t, nas) != stamp {
		t.Fatal("source tree changed")
	}
}

type countingFile struct {
	t *testing.T
	*os.File
	n *int64
}

func (c countingFile) Read(p []byte) (int, error) {
	if len(p) > importCopyBuffer {
		c.t.Fatalf("copy buffer %d", len(p))
	}
	n, err := c.File.Read(p)
	*c.n += int64(n)
	return n, err
}

type cancelOnRead struct {
	*os.File
	cancel context.CancelFunc
}

func (c cancelOnRead) Read(p []byte) (int, error) {
	c.cancel()
	return c.File.Read(p)
}

func stageBlocksOK(t *testing.T, req ImportRequest, id string, seam stageCopyHooks) StagedBlocks {
	t.Helper()
	got, err := stageImportBlocks(context.Background(), req, id, seam)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func assertNoVisibleVersion(t *testing.T, images string) {
	t.Helper()
	repo := mustStageRepo(t, images)
	if _, err := os.Stat(importStagePath(repo)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging remains: %v", err)
	}
	assertUnpublished(t, repo)
	err := filepath.WalkDir(images, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if isBlockName(d.Name()) || d.Name() == fileConcatView {
			t.Fatalf("visible %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func splitStageBodies() [][]byte {
	return [][]byte{[]byte("hsqs-block-zero"), []byte("block-one")}
}

func writeSplitLibrary(t *testing.T, bodies [][]byte, whole string) (string, ImageMeta) {
	t.Helper()
	var blocks []BlockEntry
	var all []byte
	for i, body := range bodies {
		sum := sha256.Sum256(body)
		blocks = append(blocks, BlockEntry{
			Name: fmt.Sprintf(blockNameFmt, i), Size: int64(len(body)),
			SHA256: hex.EncodeToString(sum[:]), MtimeUnix: int64(1_710_000_000 + i),
		})
		all = append(all, body...)
	}
	sum := sha256.Sum256(all)
	id := hex.EncodeToString(sum[:])
	if whole != "" {
		id = whole
	}
	node := ImageMeta{
		ID: id, Kind: KindFull, SHA256: id, Size: int64(len(all)),
		Format:    Format{Type: FormatSquashfs, Compression: CompressionZstd},
		MtimeUnix: 1, CreatedAt: time.Unix(1, 0).UTC(), Builder: "vendor",
		Blocks: blocks,
	}
	dir := filepath.Join(t.TempDir(), "lib")
	body, err := EncodeVersionManifest(VersionManifest{
		Version: VersionManifestVersion, Name: "other/lib", Images: []ImageMeta{node},
	})
	if err != nil {
		t.Fatal(err)
	}
	obj := filepath.Join(dir, dirObjects, node.ID)
	if err := os.MkdirAll(obj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileMeta), body, 0o644); err != nil {
		t.Fatal(err)
	}
	for i, block := range blocks {
		path := filepath.Join(obj, block.Name)
		if err := os.WriteFile(path, bodies[i], 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Unix(block.MtimeUnix, 0)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	concat := filepath.Join(obj, fileConcatView)
	if err := os.WriteFile(concat, []byte(oldSplitConcat), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, node
}

func relocateBlock(t *testing.T, dir string, node ImageMeta, block BlockEntry) string {
	t.Helper()
	leaf := filepath.Join(dir, dirObjects, node.ID, block.Name)
	payload := filepath.Join(t.TempDir(), "payload-"+block.Name)
	if err := os.WriteFile(payload, mustRead(t, leaf), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(block.MtimeUnix, 0)
	if err := os.Chtimes(payload, when, when); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(leaf); err != nil {
		t.Fatal(err)
	}
	if err := writePathRef(payload, leaf); err != nil {
		t.Fatal(err)
	}
	return payload
}

func removeSecondBlock(t *testing.T, dir string, node ImageMeta) {
	t.Helper()
	path := filepath.Join(dir, dirObjects, node.ID, node.Blocks[1].Name)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func swapBlockPayloads(t *testing.T, dir string, node ImageMeta) {
	t.Helper()
	obj := filepath.Join(dir, dirObjects, node.ID)
	first := filepath.Join(obj, node.Blocks[0].Name)
	second := filepath.Join(obj, node.Blocks[1].Name)
	a, b := mustRead(t, first), mustRead(t, second)
	if err := os.WriteFile(first, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, a, 0o644); err != nil {
		t.Fatal(err)
	}
}

func corruptFirstBlock(t *testing.T, dir string, node ImageMeta) {
	t.Helper()
	path := filepath.Join(dir, dirObjects, node.ID, node.Blocks[0].Name)
	body := mustRead(t, path)
	body[len(body)-1] ^= 0xff
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
