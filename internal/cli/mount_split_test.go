package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

// splitMtime is the fixed modification time every split fixture file
// carries, so the quick probe compares aligned facts. Metadata
// validation requires UTC timestamps.
var splitMtime = time.Unix(1772600767, 0).UTC()

// concatViewFile is the layout name of the concat description file
// inside a blocks payload directory.
const concatViewFile = image.ImageFile + "-concat-view"

// sumOf is the lowercase hex sha256 of one byte slice.
func sumOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeSplitFile drops one fixture file with the fixed manifest
// mtime.
func writeSplitFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, splitMtime, splitMtime); err != nil {
		t.Fatal(err)
	}
}

// storeFixtureMeta writes one repository metadata document.
func storeFixtureMeta(t *testing.T, repoDir string, meta image.Meta) {
	t.Helper()
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := image.Store(filepath.Join(repoDir, "meta.yaml"), meta); err != nil {
		t.Fatal(err)
	}
}

// fixtureMeta is the shared metadata head of the split fixtures.
func fixtureMeta(owner, repo string) image.Meta {
	return image.Meta{
		Owner: owner, Repo: repo, Version: "v1",
		Kind:      image.KindFull,
		Format:    image.Format{Type: image.FormatSquashfs, Compression: image.CompressionZstd},
		MtimeUnix: splitMtime.Unix(), CreatedAt: splitMtime,
		Builder: "tiler test",
	}
}

// writeBlocksFixture seeds one blocks-form repository: real block
// files whose sizes and mtimes agree with the manifest, optionally
// the concat description file beside them, and a valid metadata
// document. It returns the whole-image id.
func writeBlocksFixture(t *testing.T, imagesDir, name string, bodies []string, view bool) string {
	t.Helper()
	owner, repo, ok := strings.Cut(name, "/")
	if !ok {
		t.Fatalf("fixture name %q is not owner/repo", name)
	}
	var whole []byte
	for _, body := range bodies {
		whole = append(whole, body...)
	}
	wholeSum := sha256.Sum256(whole)
	meta := fixtureMeta(owner, repo)
	meta.ID = image.ID(wholeSum)
	meta.Size = int64(len(whole))
	meta.SHA256 = hex.EncodeToString(wholeSum[:])
	blocksDir := filepath.Join(imagesDir, owner, repo, "blocks", meta.ID)
	var names []string
	for i, body := range bodies {
		block := fmt.Sprintf("blk.%03d", i)
		writeSplitFile(t, filepath.Join(blocksDir, block), body)
		names = append(names, block)
		meta.Blocks = append(meta.Blocks, image.BlockEntry{
			Name: block, Size: int64(len(body)),
			SHA256: sumOf([]byte(body)), MtimeUnix: splitMtime.Unix(),
		})
	}
	if view {
		writeSplitFile(t, filepath.Join(blocksDir, concatViewFile),
			strings.Join(names, "\n")+"\n")
	}
	storeFixtureMeta(t, filepath.Join(imagesDir, owner, repo), meta)
	return meta.ID
}

// writeBlocksChainFixture seeds a blocks full image plus one chain
// layer under blocks/<incr-id>/. It returns the full and layer
// whole-image digests used as cache keys on reassembly.
func writeBlocksChainFixture(
	t *testing.T, imagesDir, name string, fullBodies, layerBodies []string,
) (fullSum, layerSum string) {
	t.Helper()
	owner, repo, ok := strings.Cut(name, "/")
	if !ok {
		t.Fatalf("fixture name %q is not owner/repo", name)
	}
	fullID := writeBlocksFixture(t, imagesDir, name, fullBodies, true)
	var layerWhole []byte
	for _, body := range layerBodies {
		layerWhole = append(layerWhole, body...)
	}
	layerDigest := sha256.Sum256(layerWhole)
	layerSum = hex.EncodeToString(layerDigest[:])
	layerID := image.ID(layerDigest)
	blocksDir := filepath.Join(imagesDir, owner, repo, "blocks", layerID)
	metaPath := filepath.Join(imagesDir, owner, repo, "meta.yaml")
	meta, err := image.Load(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != fullID {
		t.Fatalf("full id = %s, want %s", meta.ID, fullID)
	}
	entry := image.ChainEntry{
		ID: layerID, Parent: meta.ID,
		Size: int64(len(layerWhole)), SHA256: layerSum,
		MtimeUnix: splitMtime.Unix(), CreatedAt: splitMtime,
	}
	for i, body := range layerBodies {
		block := fmt.Sprintf("blk.%03d", i)
		writeSplitFile(t, filepath.Join(blocksDir, block), body)
		entry.Blocks = append(entry.Blocks, image.BlockEntry{
			Name: block, Size: int64(len(body)),
			SHA256: sumOf([]byte(body)), MtimeUnix: splitMtime.Unix(),
		})
	}
	meta.Chain = []image.ChainEntry{entry}
	storeFixtureMeta(t, filepath.Join(imagesDir, owner, repo), meta)
	return meta.SHA256, layerSum
}

func TestMountBlocksReassembleRoute(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	writeBlocksFixture(t, imagesDir, "alice/app", []string{"block-one-bytes", "tail"}, true)
	dir := filepath.Join(work, "view")

	stdout, stderr, _, err := runCmd(t, append(lib, "mount", "alice/app", dir)...)
	if err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	name, rest, ok := parseNewSessionCreated(stdout)
	if !ok || !generatedSessionName.MatchString(name) || rest != "mounted "+dir+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	// Blocks mounts materialize through reassembly.
	if f.assembleN != 1 {
		t.Fatalf("assemble calls = %d, want one materialization", f.assembleN)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d mounts", len(f.mountReqs))
	}
	req := f.mountReqs[0]
	canonical := filepath.Join(imagesDir, "alice", "app", image.ImageFile)
	wantFull := filepath.Join(home, ".tiler", "cache") + string(filepath.Separator)
	if !strings.HasPrefix(req.Full, wantFull) || !strings.HasSuffix(req.Full, image.SquashExt) {
		t.Errorf("Full = %q, want the content-addressed cache file", req.Full)
	}
	if req.FullFSName != canonical {
		t.Errorf("FullFSName = %q, want the canonical library path", req.FullFSName)
	}
	if req.Concat != nil {
		t.Errorf("the reassembly route set a concat source: %+v", req.Concat)
	}
	if req.MergedDir != dir {
		t.Errorf("merged = %s, want %s", req.MergedDir, dir)
	}
	if !strings.HasSuffix(req.RuntimeDir, filepath.Join("runtime", "m1")) ||
		!strings.HasPrefix(req.RuntimeDir, sessionsDir(home)+string(filepath.Separator)) {
		t.Errorf("runtime = %s, want under %s/.../runtime/m1",
			req.RuntimeDir, sessionsDir(home))
	}
	// A blocks payload is already materialized in the cache
	// directory, so the cache_enabled copy never stacks on top.
	if len(f.cacheCalls) != 0 {
		t.Errorf("the blocks route stacked the cache copy: %v", f.cacheCalls)
	}
}

func TestMountBlocksCacheEnabledSkipsSecondCopy(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	writeBlocksFixture(t, imagesDir, "alice/app", []string{"one", "two"}, true)
	dir := filepath.Join(work, "view")
	args := append(append([]string(nil), lib...), "--cache")
	if _, stderr, _, err := runCmd(t, append(args, "mount", "alice/app", dir)...); err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	if len(f.cacheCalls) != 0 {
		t.Errorf("cache_enabled stacked a second copy: %v", f.cacheCalls)
	}
	if f.assembleN != 1 {
		t.Errorf("assemble calls = %d", f.assembleN)
	}
}

func TestMountBlocksChainReassemblesAndStacks(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	fullSum, layerSum := writeBlocksChainFixture(t, imagesDir, "alice/app",
		[]string{"full-one", "full-two"}, []string{"incr-upper"})
	dir := filepath.Join(work, "view")

	stdout, stderr, _, err := runCmd(t, append(lib, "mount", "alice/app", dir)...)
	if err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	name, rest, ok := parseNewSessionCreated(stdout)
	if !ok || !generatedSessionName.MatchString(name) || rest != "mounted "+dir+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if f.assembleN != 2 {
		t.Fatalf("assemble calls = %d, want one per stack layer", f.assembleN)
	}
	// Reassembly walks full then chain (oldest-first); each call
	// is keyed by that layer's whole digest and byte size.
	if len(f.assembleCalls) != 2 ||
		f.assembleCalls[0][1] != fullSum || f.assembleCalls[0][2] != "16" ||
		f.assembleCalls[0][3] != "2" ||
		f.assembleCalls[1][1] != layerSum || f.assembleCalls[1][2] != "10" ||
		f.assembleCalls[1][3] != "1" {
		t.Fatalf("assemble order/identity = %v, want full %s then incr %s",
			f.assembleCalls, fullSum, layerSum)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("mount calls = %d", len(f.mountReqs))
	}
	req := f.mountReqs[0]
	cache := filepath.Join(home, ".tiler", "cache")
	wantFull := filepath.Join(cache, fullSum+image.SquashExt)
	wantLayer := filepath.Join(cache, layerSum+image.SquashExt)
	if req.Full != wantFull {
		t.Errorf("Full = %q, want %q", req.Full, wantFull)
	}
	if len(req.Layers) != 1 || req.Layers[0] != wantLayer {
		t.Errorf("Layers = %v, want [%s]", req.Layers, wantLayer)
	}
	if req.Concat != nil {
		t.Errorf("blocks stack set a concat source: %+v", req.Concat)
	}
	canonical := filepath.Join(imagesDir, "alice", "app", image.ImageFile)
	if req.FullFSName != canonical {
		t.Errorf("FullFSName = %q, want %q", req.FullFSName, canonical)
	}
}

func TestMountBlocksCorruptManifestFails(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	id := writeBlocksFixture(t, imagesDir, "alice/app", []string{"one", "two"}, true)
	// Break the recorded size fact of the first block.
	writeSplitFile(t,
		filepath.Join(imagesDir, "alice", "app", "blocks", id, "blk.000"),
		"corrupted-and-longer")
	dir := filepath.Join(work, "view")

	_, stderr, _, err := runCmd(t, append(lib, "mount", "alice/app", dir)...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	if !strings.Contains(stderr, "blk.000") {
		t.Fatalf("stderr = %q, want it to name the bad block", stderr)
	}
	if f.assembleN != 0 || len(f.mountReqs) != 0 {
		t.Errorf("the corrupt payload reached an engine: %v %v", f.assembleCalls, f.mountReqs)
	}
}

func TestMountBlocksVerifyRehashesEveryBlock(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	id := writeBlocksFixture(t, imagesDir, "alice/app", []string{"same", "size"}, true)
	// Flip the content of the second block while keeping its size
	// and mtime, so only the full re-hash catches it.
	writeSplitFile(t,
		filepath.Join(imagesDir, "alice", "app", "blocks", id, "blk.001"),
		"flip")
	dir := filepath.Join(work, "view")

	if _, stderr, _, err := runCmd(t, append(lib, "mount", "alice/app", dir)...); err != nil {
		t.Fatalf("the quick probe passed the flipped block: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("mount calls = %d", len(f.mountReqs))
	}
	f.mountReqs = nil
	f.assembleN = 0
	_, stderr, _, err := runCmd(t, append(lib, "mount", "--verify", "alice/app", dir+"2")...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	if !strings.Contains(stderr, "blk.001") {
		t.Fatalf("stderr = %q, want it to name the flipped block", stderr)
	}
	if f.assembleN != 0 || len(f.mountReqs) != 0 {
		t.Errorf("the failed verification reached an engine: %v %v", f.assembleCalls, f.mountReqs)
	}
}

func TestMountBlocksSubdirUsesReassemble(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	writeBlocksFixture(t, imagesDir, "alice/app", []string{"one", "two"}, true)
	dir := filepath.Join(work, "view")

	stdout, stderr, _, err := runCmd(t,
		append(lib, "mount", "--subdir", "src", "alice/app", dir)...)
	if err != nil {
		t.Fatalf("reassemble subdir mount: %v\n%s", err, stderr)
	}
	name, rest, ok := parseNewSessionCreated(stdout)
	if !ok || !generatedSessionName.MatchString(name) || rest != "mounted "+dir+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(f.mountReqs) != 1 || f.mountReqs[0].Subdir != "src" {
		t.Errorf("subdir request = %+v", f.mountReqs)
	}
}

func TestUmountInjectsLibraryOwnership(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	f.removed = 1
	dir := filepath.Join(work, "view")
	if _, stderr, _, err := runCmd(t, append(lib, "umount", dir)...); err != nil {
		t.Fatalf("umount: %v\n%s", err, stderr)
	}
	if len(f.umountReqs) != 1 {
		t.Fatalf("umount calls = %d", len(f.umountReqs))
	}
	assertOwnedPredicate(t, imagesDir, f.umountReqs[0].Owned)
}

func TestSessionUmountInjectsLibraryOwnership(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	f.removed = 1
	writeSession(t, sessionsDir(home), "dev", "alice", "app", "0123456789abcdef", "active")
	if _, stderr, _, err := runCmd(t, append(lib, "session", "close", "dev")...); err != nil {
		t.Fatalf("session close: %v\n%s", err, stderr)
	}
	if len(f.umountReqs) != 1 {
		t.Fatalf("umount calls = %d", len(f.umountReqs))
	}
	assertOwnedPredicate(t, imagesDir, f.umountReqs[0].Owned)
}

// assertOwnedPredicate checks the shared ownership closure: library
// sources of every shape resolve, foreign sources never do.
func assertOwnedPredicate(t *testing.T, imagesDir string, owned func(string) bool) {
	t.Helper()
	if owned == nil {
		t.Fatal("the unmount request carries no ownership predicate")
	}
	for _, source := range []string{
		filepath.Join(imagesDir, "alice", "app", image.ImageFile),
		filepath.Join(imagesDir, "alice", "app", "incr", "0123456789abcdef.sqsh"),
	} {
		if !owned(source) {
			t.Errorf("library source %s was not owned", source)
		}
	}
	for _, source := range []string{
		"/mnt/usb/tools",
		filepath.Join(imagesDir, "alice", "app", "blocks", "0123456789abcdef", "blk.000"),
		filepath.Join(imagesDir, "alice", "app", "members", "src.sqsh"),
	} {
		if owned(source) {
			t.Errorf("foreign source %s was owned", source)
		}
	}
}
