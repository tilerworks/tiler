//go:build linux

package cli

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

// repoRootDir is the checkout root, captured before tests change the
// working directory into a temporary workspace.
var repoRootDir = func() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Join(dir, "..", "..")
}()

// splitConfig is the resolved configuration of the split integration
// fixtures. Its directories match the library flags, so a library
// level build with a test-injected block size feeds the same store
// the command surface reads.
func splitConfig(home string) config.Config {
	cfg := config.Defaults(home)
	cfg.ImagesDir = filepath.Join(home, "lib", "images")
	cfg.SessionsDir = filepath.Join(home, "lib", "sessions")
	cfg.CacheDir = filepath.Join(home, "lib", "cache")
	return cfg
}

// splitSource is a multi-section tree whose incompressible bulk file
// forces the packed squashfs over one small test block.
func splitSource(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "docs", "guide.md"), "# guide\n")
	writeTestFile(t, filepath.Join(src, "src", "main.go"), "package main\n")
	bulk := make([]byte, 300*1024)
	if _, err := rand.Read(bulk); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(src, "bulk.bin"), string(bulk))
	return src
}

// buildSplitBlocks packs src as a blocks payload with a small
// test-injected block size, so a modest tree spans several blocks.
func buildSplitBlocks(t *testing.T, cfg config.Config, name, src string, blockSize int64) image.FullResult {
	t.Helper()
	res, err := image.BuildFull(image.FullRequest{
		Config: cfg, Name: name, SrcDir: src,
		Version: "v1", Builder: "test",
		BlockSize: blockSize,
		Refs:      session.Refs{Dir: cfg.SessionsDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// buildSplitMembers packs src as a members payload: one squashfs
// per top-level directory.
func buildSplitMembers(t *testing.T, cfg config.Config, name, src string) image.FullResult {
	t.Helper()
	res, err := image.BuildMembers(image.FullRequest{
		Config: cfg, Name: name, SrcDir: src,
		Version: "v1", Builder: "test",
		Refs: session.Refs{Dir: cfg.SessionsDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// payloadDigests re-hashes every file of a payload directory, keyed
// by name, so a mount can be shown to leave the library untouched.
func payloadDigests(t *testing.T, dir string) map[string]string {
	t.Helper()
	digests := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		digests[entry.Name()] = fileSHA256Hex(t, filepath.Join(dir, entry.Name()))
	}
	return digests
}

func TestSplitBlocksReassembleRoundTrip(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	cacheDir := filepath.Join(home, ".tiler", "cache")
	cfg := splitConfig(home)
	src := splitSource(t)
	wantTree := listTree(t, src)
	res := buildSplitBlocks(t, cfg, "alice/data", src, 128*1024)

	// Layout: blocks/<id>/blk.NNN plus the concat description file
	// beside them, listing the block names in order.
	blocksDir := filepath.Join(imagesDir, "alice", "data", "blocks", res.ID)
	names, err := filepath.Glob(filepath.Join(blocksDir, "blk.*"))
	if err != nil || len(names) < 2 {
		t.Fatalf("blocks = %v (%v), want a multi-block payload", names, err)
	}
	viewBytes, err := os.ReadFile(filepath.Join(blocksDir, concatViewFile))
	if err != nil {
		t.Fatalf("concat description file: %v", err)
	}
	wantLines := make([]string, 0, len(names))
	for i := range names {
		wantLines = append(wantLines, fmt.Sprintf("blk.%03d", i))
	}
	if got := strings.Split(strings.TrimSpace(string(viewBytes)), "\n"); !slices.Equal(got, wantLines) {
		t.Errorf("description lines = %q, want %q", got, wantLines)
	}

	// Manifest facts: the production resolver pairs every block
	// with its recorded size and digest, and the whole identity is
	// the concatenated byte digest.
	target, err := image.ResolveMount(cfg, "alice/data")
	if err != nil {
		t.Fatal(err)
	}
	if target.Form != image.PayloadBlocks || len(target.Blocks) != len(names) {
		t.Fatalf("target = %+v, want %d blocks", target.Form, len(target.Blocks))
	}
	for i, block := range target.Blocks {
		want := fmt.Sprintf("blk.%03d", i)
		info, statErr := os.Stat(block.Path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if block.Name != want || block.Size != info.Size() ||
			block.SHA256 != fileSHA256Hex(t, block.Path) {
			t.Errorf("block %d = %+v (stat size %d)", i, block, info.Size())
		}
	}

	// The default route mounts through a materialized cache copy.
	view := filepath.Join(work, "view")
	deferForceUmount(t, sessionsRoot, view)
	mustSessionNew(t, lib, "dev")
	mountOK(t, lib, "mounted "+view+"\n",
		"mount", "--session", "dev", "alice/data", view)

	cached := filepath.Join(cacheDir, target.SHA256+image.SquashExt)
	if got := fileSHA256Hex(t, cached); got != target.SHA256 {
		t.Errorf("materialized copy digest = %s, want %s", got, target.SHA256)
	}
	if got := listTree(t, view); !slices.Equal(got, wantTree) {
		t.Errorf("mounted view does not match the source tree:\n%v\n%v", got, wantTree)
	}
	entry, ok := entryAt(realTable(t), view)
	if !ok || entry.Upper == "" {
		t.Fatalf("no writable overlay entry at %s: %+v", view, entry)
	}

	// Writes land in the upper while every block digest stays.
	before := payloadDigests(t, blocksDir)
	writeTestFile(t, filepath.Join(view, "extra.txt"), "extra\n")
	assertBytesFile(t, filepath.Join(entry.Upper, "extra.txt"), "extra\n")
	if after := payloadDigests(t, blocksDir); !slices.Equal(sortedDigests(before), sortedDigests(after)) {
		t.Errorf("block digests changed under the mount")
	}

	// Umount is idempotent.
	if out := umountCmd(t, lib, view); out != "unmounted "+view+"\n" {
		t.Fatalf("umount output = %q", out)
	}
	if out := umountCmd(t, lib, view); out != "" {
		t.Fatalf("an idempotent umount printed %q", out)
	}

	// One session open replays the record and restores the view,
	// reusing the materialized copy and the surviving upper.
	if out := mustCmd(t, append(lib, "session", "open", "dev")...); out != "opened dev\n" {
		t.Fatalf("open output = %q", out)
	}
	assertBytesFile(t, filepath.Join(view, "extra.txt"), "extra\n")
	assertBytesFile(t, filepath.Join(view, "docs", "guide.md"), "# guide\n")
	rec := loadSessionRecord(t, sessionsDir(home), "dev")
	if len(rec.Actions) != 1 || rec.Actions[0].Status != session.StatusMounted {
		t.Errorf("record = %+v", rec.Actions)
	}

	if out := umountCmd(t, lib, view); out != "unmounted "+view+"\n" {
		t.Fatalf("final umount output = %q", out)
	}
	assertNoResidue(t, sessionsRoot, view)
}

// sortedDigests flattens a digest map into sorted lines for compare.
func sortedDigests(digests map[string]string) []string {
	lines := make([]string, 0, len(digests))
	for name, sum := range digests {
		lines = append(lines, name+":"+sum)
	}
	slices.Sort(lines)
	return lines
}

// pkgConfigFlags returns one pkg-config expansion or skips the test
// when the fuse3 development package is unavailable.
func pkgConfigFlags(t *testing.T, flag string) []string {
	t.Helper()
	out, err := exec.Command("pkg-config", flag, "fuse3").Output()
	if err != nil {
		t.Skipf("pkg-config %s fuse3: %v", flag, err)
	}
	return strings.Fields(string(out))
}

// requireConcatfs returns a usable concatfs: the one on PATH, or one
// built from the contrib sources into a test temporary directory
// that is prepended to PATH — never installed system-wide. Missing
// tool and missing build prerequisites skip the test.
func requireConcatfs(t *testing.T) string {
	t.Helper()
	if path, err := exec.LookPath("concatfs"); err == nil {
		return path
	}
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skipf("concatfs is not on PATH and gcc is missing: %v", err)
	}
	source := filepath.Join(repoRootDir, "contrib", "concatfs", "concatfs.c")
	if _, err := os.Stat(source); err != nil {
		t.Skipf("contrib concatfs source is unavailable: %v", err)
	}
	args := append([]string{"-O2", "-Wno-unused-function", source}, pkgConfigFlags(t, "--cflags")...)
	args = append(args, pkgConfigFlags(t, "--libs")...)
	bin := t.TempDir()
	target := filepath.Join(bin, "concatfs")
	args = append(args, "-o", target)
	if out, err := exec.Command("gcc", args...).CombinedOutput(); err != nil {
		t.Skipf("building concatfs from contrib failed: %v: %s", err, out)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return target
}

func TestSplitBlocksConcatRouteRoundTrip(t *testing.T) {
	t.Skip("concat route is no longer selected from configuration")
	requireMountTestEnv(t)
	requireConcatfs(t)
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	cacheDir := filepath.Join(home, ".tiler", "cache")
	cfg := splitConfig(home)
	src := splitSource(t)
	wantTree := listTree(t, src)
	res := buildSplitBlocks(t, cfg, "alice/data", src, 128*1024)
	target, err := image.ResolveMount(cfg, "alice/data")
	if err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(imagesDir, "alice", "data", image.ImageFile)
	blocksDir := filepath.Join(imagesDir, "alice", "data", "blocks", res.ID)

	view := filepath.Join(work, "concat-view")
	deferForceUmount(t, sessionsRoot, view)
	concatLib := concatRouteArgs(lib)
	mustSessionNew(t, lib, "dev")
	mountOK(t, concatLib, "mounted "+view+"\n",
		"mount", "--read-only", "--session", "dev", "alice/data", view)

	// Zero reassembly: no assembled copy appears in the cache.
	if entries, readErr := os.ReadDir(cacheDir); readErr == nil {
		for _, entry := range entries {
			if entry.Name() == target.SHA256+image.SquashExt {
				t.Fatal("the concat route materialized a cache copy")
			}
		}
	}

	// The mount table shows the canonical identity at the target
	// and the concat transit point under the runtime tree.
	entry, ok := entryAt(realTable(t), view)
	if !ok || entry.Source != canonical {
		t.Fatalf("entry at %s = %+v, want fsname %s", view, entry, canonical)
	}
	transit := ""
	for _, m := range entriesUnder(realTable(t), sessionsRoot) {
		if filepath.Base(m.Point) == "concat" {
			transit = m.Point
		}
	}
	if transit == "" {
		t.Fatalf("no concat transit point under %s", sessionsRoot)
	}
	// The virtual concatenated file hashes to the whole image
	// digest without any materialization.
	virtual := filepath.Join(transit, concatViewFile)
	if got := fileSHA256Hex(t, virtual); got != target.SHA256 {
		t.Errorf("virtual concat digest = %s, want %s", got, target.SHA256)
	}
	if got := listTree(t, view); !slices.Equal(got, wantTree) {
		t.Errorf("mounted view does not match the source tree")
	}

	// Killing both daemons of the combination: one session open
	// recovers the whole route.
	killDaemon(t, findDaemon(t, "squashfuse_ll", view))
	killDaemon(t, findDaemon(t, "concatfs", blocksDir))
	waitStale(t, view)
	if out := mustCmd(t, append(concatLib, "session", "open", "dev")...); out != "opened dev\n" {
		t.Fatalf("open output = %q", out)
	}
	if got := listTree(t, view); !slices.Equal(got, wantTree) {
		t.Errorf("recovered view does not match the source tree")
	}
	entry, ok = entryAt(realTable(t), view)
	if !ok || entry.Source != canonical {
		t.Fatalf("recovered entry at %s = %+v, want fsname %s", view, entry, canonical)
	}

	// A repeated open keeps the same daemons serving.
	pid := findDaemon(t, "squashfuse_ll", view)
	if out := mustCmd(t, append(concatLib, "session", "open", "dev")...); out != "opened dev\n" {
		t.Fatalf("repeated open output = %q", out)
	}
	if again := findDaemon(t, "squashfuse_ll", view); again != pid {
		t.Errorf("the healthy combination was remounted: %d -> %d", pid, again)
	}

	// The path umount tears the whole combination down.
	if out := umountCmd(t, concatLib, view); out != "unmounted "+view+"\n" {
		t.Fatalf("umount output = %q", out)
	}
	assertNoResidue(t, sessionsRoot, view)
	if out := umountCmd(t, concatLib, view); out != "" {
		t.Fatalf("an idempotent umount printed %q", out)
	}
}

func TestSplitBlocksConcatDegradesToReassembly(t *testing.T) {
	t.Skip("concat route is no longer selected from configuration")
	requireMountTestEnv(t)
	requireConcatfs(t)
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	cacheDir := filepath.Join(home, ".tiler", "cache")
	cfg := splitConfig(home)
	src := splitSource(t)
	wantTree := listTree(t, src)
	buildSplitBlocks(t, cfg, "alice/data", src, 128*1024)
	target, err := image.ResolveMount(cfg, "alice/data")
	if err != nil {
		t.Fatal(err)
	}

	// Concat selection left configuration with the global split
	// flags; this skipped test kept a PATH probe seam that no
	// longer exists. A later mount step can restore concat choice.
	view := filepath.Join(work, "degraded")
	deferForceUmount(t, sessionsRoot, view)
	stdout, stderr, _, err := runCmd(t, append(concatRouteArgs(lib),
		"mount", "alice/data", view)...)
	if err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	if stdout != "mounted "+view+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	for _, want := range []string{
		"concatfs", "reassembly", "contrib/concatfs", "never installs it automatically",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("guidance = %q, want it to contain %q", stderr, want)
		}
	}
	// The degrade materialized the content-addressed cache copy and
	// the view serves the source tree.
	cached := filepath.Join(cacheDir, target.SHA256+image.SquashExt)
	if got := fileSHA256Hex(t, cached); got != target.SHA256 {
		t.Errorf("materialized copy digest = %s, want %s", got, target.SHA256)
	}
	if got := listTree(t, view); !slices.Equal(got, wantTree) {
		t.Errorf("the degraded view does not match the source tree")
	}
	if out := umountCmd(t, lib, view); out != "unmounted "+view+"\n" {
		t.Fatalf("umount output = %q", out)
	}
	assertNoResidue(t, sessionsRoot, view)
}

// forestSource is a members-form source tree: plain directory
// sections plus a git working tree member.
func forestSource(t *testing.T) (string, string) {
	t.Helper()
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "docs", "guide.md"), "# guide\n")
	writeTestFile(t, filepath.Join(src, "docs", "api.md"), "# api\n")
	writeTestFile(t, filepath.Join(src, "code", "main.go"), "package main\n")
	worktree := filepath.Join(src, "worktree")
	writeTestFile(t, filepath.Join(worktree, "README.md"), "# repo\n")
	runGit(t, worktree, "init", "-b", "main")
	runGit(t, worktree, "add", "-A")
	runGit(t, worktree, "-c", "user.name=test", "-c", "user.email=test@example.com",
		"commit", "-m", "initial import")
	head := strings.TrimSpace(runGit(t, worktree, "rev-parse", "HEAD"))
	return src, head
}

func TestSplitMembersForestRoundTrip(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	cfg := splitConfig(home)
	src, wantHead := forestSource(t)
	res := buildSplitMembers(t, cfg, "alice/forest", src)

	// Layout: one squashfs per top-level directory and manifest
	// facts that match the real files.
	membersDir := filepath.Join(imagesDir, "alice", "forest", image.MembersDir)
	target, err := image.ResolveMount(cfg, "alice/forest")
	if err != nil {
		t.Fatal(err)
	}
	if target.Form != image.PayloadMembers || target.ID != res.ID {
		t.Fatalf("target = %+v", target)
	}
	wantNames := []string{"code", "docs", "worktree"}
	if len(target.Partitions) != len(wantNames) {
		t.Fatalf("partitions = %+v", target.Partitions)
	}
	for i, p := range target.Partitions {
		if p.Name != wantNames[i] {
			t.Errorf("partition %d = %s, want %s", i, p.Name, wantNames[i])
		}
		info, statErr := os.Stat(p.Path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if p.Size != info.Size() || p.SHA256 != fileSHA256Hex(t, p.Path) {
			t.Errorf("partition %s facts do not match the file", p.Name)
		}
	}
	wantTrees := map[string][]string{
		"code":     listTree(t, filepath.Join(src, "code")),
		"docs":     listTree(t, filepath.Join(src, "docs")),
		"worktree": listTree(t, filepath.Join(src, "worktree")),
	}

	// The forest mounts read-only under one parent directory.
	parent := filepath.Join(work, "forest")
	deferForceUmount(t, sessionsRoot,
		filepath.Join(parent, "code"),
		filepath.Join(parent, "docs"),
		filepath.Join(parent, "worktree"))
	mustSessionNew(t, lib, "dev")
	mountOK(t, lib, "mounted "+parent+"\n",
		"mount", "--read-only", "--session", "dev", "alice/forest", parent)

	// The parent's top level is exactly the member name set, and
	// every member subtree matches its source byte for byte.
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	gotNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotNames = append(gotNames, entry.Name())
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("forest top level = %v, want %v", gotNames, wantNames)
	}
	for name, want := range wantTrees {
		if got := listTree(t, filepath.Join(parent, name)); !slices.Equal(got, want) {
			t.Errorf("member %s does not match its source:\n%v\n%v", name, got, want)
		}
	}
	// The git worktree member answers through the mount.
	if got := strings.TrimSpace(runGit(t, filepath.Join(parent, "worktree"),
		"rev-parse", "HEAD")); got != wantHead {
		t.Errorf("mounted HEAD = %s, want %s", got, wantHead)
	}
	// The forest is read-only.
	if err := os.WriteFile(filepath.Join(parent, "docs", "new.md"), []byte("x"), 0o644); err == nil {
		t.Error("the read-only forest accepted a write")
	}
	// The record carries one combination action at the parent.
	rec := loadSessionRecord(t, sessionsDir(home), "dev")
	if len(rec.Actions) != 1 {
		t.Fatalf("actions = %+v", rec.Actions)
	}
	action := rec.Actions[0]
	if action.Mode != session.ModeRO || action.Mountpoint != parent ||
		action.Owner != "alice" || action.Repo != "forest" {
		t.Errorf("action = %+v", action)
	}

	// The status overview annotates the parent combination through
	// the real health probes: one healthy action, and the forest
	// leaves no orphan candidates without a runtime trace.
	stdout := mustCmd(t, append(lib, "status")...)
	assertStatusOverview(t, stdout, "dev active", 1, "health=healthy", 0)

	// A repeated open is a pure no-op: the same daemons keep serving.
	pids := map[string]int{}
	for _, name := range wantNames {
		pids[name] = findDaemon(t, "squashfuse_ll", filepath.Join(parent, name))
	}
	if out := mustCmd(t, append(lib, "session", "open", "dev")...); out != "opened dev\n" {
		t.Fatalf("open output = %q", out)
	}
	for _, name := range wantNames {
		if again := findDaemon(t, "squashfuse_ll", filepath.Join(parent, name)); again != pids[name] {
			t.Errorf("member %s was remounted by a no-op open: %d -> %d", name, pids[name], again)
		}
	}

	// Killing one member daemon: the next open repairs that member
	// and reuses the rest. The open probe is the reliable death
	// signal — the kernel attribute cache can keep stat answering.
	killDaemon(t, pids["docs"])
	waitOverlayDead(t, filepath.Join(parent, "docs"))
	if out := mustCmd(t, append(lib, "session", "open", "dev")...); out != "opened dev\n" {
		t.Fatalf("repair open output = %q", out)
	}
	if again := findDaemon(t, "squashfuse_ll", filepath.Join(parent, "docs")); again == pids["docs"] {
		t.Error("the dead member was not remounted")
	}
	for _, name := range []string{"code", "worktree"} {
		if again := findDaemon(t, "squashfuse_ll", filepath.Join(parent, name)); again != pids[name] {
			t.Errorf("healthy member %s was remounted: %d -> %d", name, pids[name], again)
		}
	}
	if got := listTree(t, filepath.Join(parent, "docs")); !slices.Equal(got, wantTrees["docs"]) {
		t.Error("the repaired member does not match its source")
	}

	// A deep path inside a member reverse-looks up to the session
	// through the parent combination, on the command surface and
	// through the engine seam.
	deep := filepath.Join(parent, "docs", "guide.md")
	stdout = mustCmd(t, append(lib, "status", deep)...)
	if !strings.HasPrefix(stdout, "dev active mountpoint="+parent+"\n") {
		t.Fatalf("deep member status = %q", stdout)
	}
	engine := &session.Engine{
		Store:       session.Store{Dir: sessionsDir(home)},
		LoadTable:   mountinfo.Load,
		SessionsDir: sessionsDir(home),
		ImagesDir:   imagesDir,
	}
	hit, err := engine.SessionForPath(deep)
	if err != nil {
		t.Fatalf("SessionForPath: %v", err)
	}
	if hit.Record.Name != "dev" || hit.Mountpoint != parent || hit.Stale {
		t.Fatalf("hit = %+v", hit)
	}

	// The parent umount tears the whole forest down with no
	// residue, and the member digests survive.
	before := payloadDigests(t, membersDir)
	if out := umountCmd(t, lib, parent); out != "unmounted "+parent+"\n" {
		t.Fatalf("umount output = %q", out)
	}
	table := realTable(t)
	if _, listed := entryAt(table, parent); listed {
		t.Error("the mount table still lists the parent")
	}
	if stray := entriesUnder(table, parent); len(stray) != 0 {
		t.Errorf("member mounts survived: %+v", stray)
	}
	if after := payloadDigests(t, membersDir); !slices.Equal(sortedDigests(before), sortedDigests(after)) {
		t.Error("member digests changed across the mount")
	}
	if out := umountCmd(t, lib, parent); out != "" {
		t.Fatalf("an idempotent umount printed %q", out)
	}
}

func TestSplitGuidanceWalkthrough(t *testing.T) {
	requireMountTestEnv(t)
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	cacheDir := filepath.Join(home, "lib", "cache")
	for _, dir := range []string{imagesDir, cacheDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	doctorLib := append(append([]string(nil), lib...), "--cache-dir", cacheDir)

	// Doctor probes the temp-configured directories: the limit
	// ladder runs and reports on both. The overall verdict may
	// carry environment-caused tool failures, which the full-probe
	// test judges; the walkthrough only reads the report.
	stdout, _, _, _ := runCmd(t, append(doctorLib, "doctor")...)
	lines := parseDoctorLines(t, stdout)
	for _, name := range []string{"file_limit_images", "file_limit_cache"} {
		line, ok := lines[name]
		if !ok {
			t.Fatalf("probe %s missing:\n%s", name, stdout)
		}
		if line.status != "pass" && line.status != "warn" && line.status != "info" {
			t.Errorf("probe %s status = %q", name, line.status)
		}
		if !strings.Contains(line.detail, "single-file size limit") {
			t.Errorf("probe %s detail = %q", name, line.detail)
		}
	}

	// Split stays off, so the command surface still publishes one file.
	src := subdirSource(t)
	stdout = mustCmd(t, append(lib, "build", "--image", "alice/data", src)...)
	fields := strings.Fields(stdout)
	if len(fields) != 3 || fields[0] != "full" || fields[1] != "alice/data" {
		t.Fatalf("build output = %q", stdout)
	}
	imagePath := filepath.Join(imagesDir, "alice", "data", image.ImageFile)
	if _, err := os.Stat(imagePath); err != nil {
		t.Errorf("the build left no single-file image: %v", err)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, "alice", "data", "blocks")); !os.IsNotExist(err) {
		t.Errorf("split-off build published blocks: %v", err)
	}

	// With split off, the doctor limit probe still reports the clean
	// unlimited shape.
	stdout, _, _, _ = runCmd(t, append(append([]string(nil), lib...),
		"--cache-dir", cacheDir, "doctor")...)
	lines = parseDoctorLines(t, stdout)
	if line := lines["file_limit_images"]; line.status != "pass" ||
		!strings.Contains(line.detail, "single-file size limit") {
		t.Errorf("file_limit_images = %+v, want the clean limit report", line)
	}
}
