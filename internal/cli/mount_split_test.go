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
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
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

// fixtureMember is one members-form partition with its file bytes.
type fixtureMember struct {
	name string
	body string
}

// writeMembersFixture seeds one members-form repository: a real
// squashfs file per partition and a valid metadata document whose
// whole identity is the derived manifest digest. It returns the id.
func writeMembersFixture(t *testing.T, imagesDir, name string, members []fixtureMember) string {
	t.Helper()
	owner, repo, ok := strings.Cut(name, "/")
	if !ok {
		t.Fatalf("fixture name %q is not owner/repo", name)
	}
	meta := fixtureMeta(owner, repo)
	membersDir := filepath.Join(imagesDir, owner, repo, "members")
	var digestText []byte
	for _, m := range members {
		writeSplitFile(t, filepath.Join(membersDir, m.name+image.SquashExt), m.body)
		entry := image.MemberEntry{
			Name: m.name, Image: image.MembersDir + "/" + m.name + image.SquashExt,
			Size: int64(len(m.body)), SHA256: sumOf([]byte(m.body)),
			MtimeUnix: splitMtime.Unix(),
		}
		meta.Members = append(meta.Members, entry)
		meta.Size += entry.Size
		digestText = append(digestText,
			fmt.Sprintf("%s %d %s\n", entry.Name, entry.Size, entry.SHA256)...)
	}
	digest := sha256.Sum256(digestText)
	meta.SHA256 = hex.EncodeToString(digest[:])
	meta.ID = image.ID(digest)
	storeFixtureMeta(t, filepath.Join(imagesDir, owner, repo), meta)
	return meta.ID
}

// concatRouteArgs used to append the removed global split-route
// flags. The concat selection path is gone; skipped concat tests
// keep this helper as a plain library-flag copy until a later mount
// step redefines how concat is chosen.
func concatRouteArgs(lib []string) []string {
	return append([]string(nil), lib...)
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
	if stdout != "mounted "+dir+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	// Blocks mounts materialize through reassembly.
	if f.assembleN != 1 {
		t.Fatalf("assemble calls = %d, want one materialization", f.assembleN)
	}
	if len(f.mountReqs) != 1 || len(f.forestReqs) != 0 {
		t.Fatalf("engine calls = %d mounts, %d forests", len(f.mountReqs), len(f.forestReqs))
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

func TestMountBlocksConcatRoute(t *testing.T) {
	t.Skip("concat route is no longer selected from configuration")
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	id := writeBlocksFixture(t, imagesDir, "alice/app", []string{"one", "two"}, true)
	dir := filepath.Join(work, "view")

	stdout, stderr, _, err := runCmd(t,
		append(concatRouteArgs(lib), "mount", "alice/app", dir)...)
	if err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	if stdout != "mounted "+dir+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if f.assembleN != 0 {
		t.Errorf("the concat route materialized: %v", f.assembleCalls)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("mount calls = %d", len(f.mountReqs))
	}
	req := f.mountReqs[0]
	canonical := filepath.Join(imagesDir, "alice", "app", image.ImageFile)
	if req.Full != canonical || req.FullFSName != canonical {
		t.Errorf("Full = %q fsname = %q, want the canonical identity", req.Full, req.FullFSName)
	}
	if req.Concat == nil {
		t.Fatal("the concat route did not set a concat source")
	}
	wantDir := filepath.Join(imagesDir, "alice", "app", "blocks", id)
	if req.Concat.BlocksDir != wantDir || req.Concat.ViewName != concatViewFile {
		t.Errorf("concat source = %+v, want %s and %s", req.Concat, wantDir, concatViewFile)
	}
	if stderr != "" {
		t.Errorf("the in-place concat route printed guidance: %q", stderr)
	}
}

func TestMountBlocksConcatDegradesWithoutTool(t *testing.T) {
	t.Skip("concat route is no longer selected from configuration")
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	writeBlocksFixture(t, imagesDir, "alice/app", []string{"one", "two"}, true)
	dir := filepath.Join(work, "view")

	stdout, stderr, _, err := runCmd(t,
		append(concatRouteArgs(lib), "mount", "alice/app", dir)...)
	if err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	if stdout != "mounted "+dir+"\n" {
		t.Fatalf("stdout = %q, want the degraded mount to succeed", stdout)
	}
	for _, want := range []string{
		"concatfs", "reassembly", "contrib/concatfs", "never installs it automatically",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("guidance = %q, want it to contain %q", stderr, want)
		}
	}
	if f.assembleN != 1 {
		t.Errorf("the degrade did not materialize: %v", f.assembleCalls)
	}
	if len(f.mountReqs) != 1 || f.mountReqs[0].Concat != nil {
		t.Errorf("the degraded mount kept a concat source: %+v", f.mountReqs)
	}
}

func TestMountBlocksConcatMissingDescriptionFails(t *testing.T) {
	t.Skip("concat route is no longer selected from configuration")
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	writeBlocksFixture(t, imagesDir, "alice/app", []string{"one", "two"}, false)
	dir := filepath.Join(work, "view")

	_, stderr, _, err := runCmd(t,
		append(concatRouteArgs(lib), "mount", "alice/app", dir)...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	if !strings.Contains(stderr, "concat") || !strings.Contains(stderr, "description file") {
		t.Fatalf("stderr = %q, want it to name the missing description file", stderr)
	}
	// An explicit route choice with the tool in place never
	// silently degrades.
	if f.assembleN != 0 || len(f.mountReqs) != 0 {
		t.Errorf("the missing description degraded silently: %v %v", f.assembleCalls, f.mountReqs)
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

func TestMountMembersForestWiring(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	writeMembersFixture(t, imagesDir, "alice/forest", []fixtureMember{
		{name: "docs", body: "docs-bytes"},
		{name: "src", body: "src-bytes-longer"},
	})
	dir := filepath.Join(work, "forest")

	stdout, stderr, _, err := runCmd(t,
		append(lib, "mount", "--read-only", "alice/forest", dir)...)
	if err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	if stdout != "mounted "+dir+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(f.forestReqs) != 1 {
		t.Fatalf("forest calls = %d", len(f.forestReqs))
	}
	if len(f.mountReqs) != 0 || f.assembleN != 0 {
		t.Errorf("the forest route used the stack engine: %v %v", f.mountReqs, f.assembleCalls)
	}
	req := f.forestReqs[0]
	if req.ParentDir != dir {
		t.Errorf("ParentDir = %q, want %q", req.ParentDir, dir)
	}
	want := []workspace.ForestMember{
		{Name: "docs", Path: filepath.Join(imagesDir, "alice", "forest", "members", "docs.sqsh")},
		{Name: "src", Path: filepath.Join(imagesDir, "alice", "forest", "members", "src.sqsh")},
	}
	if len(req.Partitions) != 2 {
		t.Fatalf("partitions = %+v", req.Partitions)
	}
	for i, m := range req.Partitions {
		if m != want[i] {
			t.Errorf("partition %d = %+v, want %+v", i, m, want[i])
		}
	}
	if req.Owned == nil {
		t.Fatal("the forest request carries no ownership predicate")
	}
	if !req.Owned(want[0].Path) || req.Owned("/mnt/usb/tools") {
		t.Error("the ownership predicate does not resolve library sources")
	}
	if req.Run == nil || req.Table == nil {
		t.Error("the runner or table seam is not wired")
	}

	// The recorded action keeps the combination shape: one action
	// at the parent directory, read-only, with the library identity.
	records, err := (session.Store{Dir: sessionsDir(home)}).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || len(records[0].Actions) != 1 {
		t.Fatalf("records = %+v, want one implicit session with one action", records)
	}
	action := records[0].Actions[0]
	if action.Mode != session.ModeRO || action.Mountpoint != dir ||
		action.Owner != "alice" || action.Repo != "forest" ||
		action.Status != session.StatusMounted {
		t.Errorf("action = %+v", action)
	}

	// A reused forest prints the reuse verb.
	f.forestReused = true
	stdout, stderr, _, err = runCmd(t,
		append(lib, "mount", "--read-only", "alice/forest", dir)...)
	if err != nil {
		t.Fatalf("remount: %v\n%s", err, stderr)
	}
	if stdout != "already mounted "+dir+"\n" {
		t.Fatalf("stdout = %q, want the reuse verb", stdout)
	}
}

func TestMountMembersUsageRefusals(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	writeMembersFixture(t, imagesDir, "alice/forest", []fixtureMember{
		{name: "docs", body: "docs-bytes"},
	})
	dir := filepath.Join(work, "forest")

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "writable forest",
			args: []string{"mount", "alice/forest", dir},
			want: "--read-only",
		},
		{
			name: "forest with a subdir",
			args: []string{"mount", "--read-only", "--subdir", "docs", "alice/forest", dir},
			want: "--subdir",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, _, err := runCmd(t, append(lib, tt.args...)...)
			if ExitCode(err) != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
			}
			if !strings.Contains(stderr, tt.want) {
				t.Fatalf("stderr = %q, want guidance naming %s", stderr, tt.want)
			}
		})
	}
	if len(f.forestReqs) != 0 {
		t.Errorf("a refusal reached the engine: %+v", f.forestReqs)
	}
	// A usage refusal records nothing.
	records, err := (session.Store{Dir: sessionsDir(home)}).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("the refusals created session records: %+v", records)
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
	if stdout != "mounted "+dir+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(f.mountReqs) != 1 || f.mountReqs[0].Subdir != "src" {
		t.Errorf("subdir request = %+v", f.mountReqs)
	}
}

func TestMountMembersCorruptMemberFails(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	writeMembersFixture(t, imagesDir, "alice/forest", []fixtureMember{
		{name: "docs", body: "docs-bytes"},
		{name: "src", body: "src-bytes"},
	})
	writeSplitFile(t,
		filepath.Join(imagesDir, "alice", "forest", "members", "src.sqsh"),
		"src-bytes-grew")
	dir := filepath.Join(work, "forest")

	_, stderr, _, err := runCmd(t,
		append(lib, "mount", "--read-only", "alice/forest", dir)...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	if !strings.Contains(stderr, "src") {
		t.Fatalf("stderr = %q, want it to name the bad member", stderr)
	}
	if len(f.forestReqs) != 0 {
		t.Errorf("the corrupt member reached the engine: %+v", f.forestReqs)
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
		filepath.Join(imagesDir, "alice", "app", "members", "src.sqsh"),
		filepath.Join(imagesDir, "alice", "app", "incr", "0123456789abcdef.sqsh"),
	} {
		if !owned(source) {
			t.Errorf("library source %s was not owned", source)
		}
	}
	for _, source := range []string{
		"/mnt/usb/tools",
		filepath.Join(imagesDir, "alice", "app", "blocks", "0123456789abcdef", "blk.000"),
	} {
		if owned(source) {
			t.Errorf("foreign source %s was owned", source)
		}
	}
}

func TestSessionEngineCarriesImagesDir(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	var engine *session.Engine
	withSessionSeams(t, func(e *session.Engine) { engine = e })
	if _, stderr, _, err := runCmd(t, append(lib, "session", "list")...); err != nil {
		t.Fatalf("session list: %v\n%s", err, stderr)
	}
	if engine == nil {
		t.Fatal("the engine seam was not used")
	}
	if engine.ImagesDir != imagesDir {
		t.Errorf("ImagesDir = %q, want %q", engine.ImagesDir, imagesDir)
	}
}
