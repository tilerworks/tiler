package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
)

// cacheCall records one ensureCache invocation.
type cacheCall struct {
	dir  string
	src  string
	want string
	out  string
}

// mountFake replaces the engine seams so command tests assert the
// request assembly, the output lines, and the exit classes without
// real FUSE mounts.
type mountFake struct {
	table      []mountinfo.Mount
	cacheCalls []cacheCall
	toolCalls  [][]string
	mountReqs  []workspace.Request
	umountReqs []workspace.UnmountRequest
	reused     bool
	removed    int
	mountErr   error
	umountErr  error

	forestReqs   []workspace.ForestRequest
	forestReused bool
	forestErr    error

	assembleCalls [][]string
	assembleN     int
	assembleOut   string
	assembleErr   error
}

func (f *mountFake) cache(cacheDir, src, want string) (string, string, error) {
	out := filepath.Join(cacheDir, "cached-"+filepath.Base(src))
	f.cacheCalls = append(f.cacheCalls, cacheCall{dir: cacheDir, src: src, want: want, out: out})
	return out, strings.Repeat("ab", 32), nil
}

func (f *mountFake) runTool(name string, args ...string) error {
	f.toolCalls = append(f.toolCalls, append([]string{name}, args...))
	return nil
}

func (f *mountFake) mount(req workspace.Request) (*workspace.Workspace, error) {
	f.mountReqs = append(f.mountReqs, req)
	if f.mountErr != nil {
		return nil, f.mountErr
	}
	return &workspace.Workspace{MergedDir: req.MergedDir, Reused: f.reused}, nil
}

func (f *mountFake) umount(req workspace.UnmountRequest) (int, error) {
	f.umountReqs = append(f.umountReqs, req)
	if f.umountErr != nil {
		return 0, f.umountErr
	}
	return f.removed, nil
}

func (f *mountFake) forest(req workspace.ForestRequest) (*workspace.Workspace, error) {
	f.forestReqs = append(f.forestReqs, req)
	if f.forestErr != nil {
		return nil, f.forestErr
	}
	return &workspace.Workspace{MergedDir: req.ParentDir, Reused: f.forestReused}, nil
}

// assemble records one materialization call and returns the
// content-addressed cache path the real assembler would produce.
func (f *mountFake) assemble(
	cacheDir string,
	blocks []image.BlockFile,
	wantSHA string,
	wantSize int64,
) (string, error) {
	f.assembleN++
	f.assembleCalls = append(f.assembleCalls, []string{cacheDir, wantSHA, fmt.Sprint(wantSize), fmt.Sprint(len(blocks))})
	if f.assembleErr != nil {
		return "", f.assembleErr
	}
	out := f.assembleOut
	if out == "" {
		out = filepath.Join(cacheDir, wantSHA+image.SquashExt)
	}
	return out, nil
}

// withMountSeams installs the fake engines, tool runner, cache, and
// mount table for the command tests.
func withMountSeams(t *testing.T, table []mountinfo.Mount) *mountFake {
	t.Helper()
	withSessionLockSeam(t)
	f := &mountFake{table: table}
	origRun, origCache, origTable := mountRun, ensureCache, loadMountTable
	origMount, origUmount := mountEngine, unmountEngine
	origForest, origAssemble := forestEngine, assembleBlocks
	t.Cleanup(func() {
		mountRun, ensureCache, loadMountTable = origRun, origCache, origTable
		mountEngine, unmountEngine = origMount, origUmount
		forestEngine, assembleBlocks = origForest, origAssemble
	})
	mountRun = f.runTool
	ensureCache = f.cache
	loadMountTable = func() ([]mountinfo.Mount, error) { return f.table, nil }
	mountEngine = f.mount
	unmountEngine = f.umount
	forestEngine = f.forest
	assembleBlocks = f.assemble
	return f
}

// withSessionLockSeam makes the session engine's advisory lock a
// no-op, so command tests stay hermetic and run on any platform;
// the real flock has its own Linux tests.
func withSessionLockSeam(t *testing.T) {
	t.Helper()
	withSessionSeams(t, nil)
}

// withSessionSeams installs the no-op advisory lock and an optional
// engine mutator for the session command tests. The mutator lets a
// test pin the health seam, whose real probe is Linux-only, while
// the fake records reference mountpoints that do not exist.
func withSessionSeams(t *testing.T, tune func(*session.Engine)) {
	t.Helper()
	original := sessionEngine
	t.Cleanup(func() { sessionEngine = original })
	sessionEngine = func(cfg config.Config) *session.Engine {
		engine := original(cfg)
		engine.Lock = func(string, time.Duration) (func(), error) {
			return func() {}, nil
		}
		if tune != nil {
			tune(engine)
		}
		return engine
	}
}

// healthySessions pins the engine health seam to "everything is
// healthy", so cleanup never reports the fixture mountpoints stale.
func healthySessions(engine *session.Engine) {
	engine.Health = func(string) bool { return true }
}

// buildLibrary publishes one full image through the build command
// and returns its library directory.
func buildLibrary(t *testing.T, home string, lib []string, imagesDir, name string) string {
	t.Helper()
	work := filepath.Join(home, "work")
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	withBuildSeams(t, nil)
	buildImage(t, lib, name, src)
	owner, repo, _ := strings.Cut(name, "/")
	return filepath.Join(imagesDir, owner, repo)
}

func TestMountLibraryImagePrintsMounted(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	repoDir := buildLibrary(t, home, lib, imagesDir, "alice/app")
	f := withMountSeams(t, nil)
	view := filepath.Join(work, "view")

	stdout, stderr, _, err := runCmd(t, append(lib, "mount", "alice/app", view)...)
	if err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	if stdout != "mounted "+view+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d", len(f.mountReqs))
	}
	req := f.mountReqs[0]
	if req.Full != filepath.Join(repoDir, "image.sqsh") || len(req.Layers) != 0 {
		t.Errorf("stack = %s %v", req.Full, req.Layers)
	}
	if req.ReadOnly || req.Subdir != "" || req.FullFSName != "" {
		t.Errorf("flags leaked: %+v", req)
	}
	if req.MergedDir != view {
		t.Errorf("merged = %s, want %s", req.MergedDir, view)
	}
	// Implicit auto-* session name varies; require sessions/<auto>/runtime/m1.
	if !strings.HasSuffix(req.RuntimeDir, filepath.Join("runtime", "m1")) ||
		!strings.HasPrefix(req.RuntimeDir, sessionsDir(home)+string(filepath.Separator)) {
		t.Errorf("runtime = %s, want under %s/.../runtime/m1",
			req.RuntimeDir, sessionsDir(home))
	}
	if req.Run == nil {
		t.Fatal("the tool runner seam is not wired")
	}
	if err := req.Run("probe-tool"); err != nil {
		t.Fatal(err)
	}
	if len(f.toolCalls) != 1 || f.toolCalls[0][0] != "probe-tool" {
		t.Errorf("tool calls = %v", f.toolCalls)
	}
	if len(f.cacheCalls) != 0 {
		t.Errorf("the cache ran although it is disabled: %v", f.cacheCalls)
	}
}

func TestMountFlagsReachTheEngine(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	buildLibrary(t, home, lib, imagesDir, "alice/app")
	f := withMountSeams(t, nil)
	view := filepath.Join(work, "view")
	args := append(lib, "mount", "--read-only", "--subdir", "src/lib",
		"--squashfs-options", "sopt", "--fuse-options", "fopt", "alice/app", view)

	if _, stderr, _, err := runCmd(t, args...); err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	req := f.mountReqs[0]
	if !req.ReadOnly || req.Subdir != "src/lib" {
		t.Errorf("mode flags = %+v", req)
	}
	if req.SquashfsOptions != "sopt" || req.FuseOptions != "fopt" {
		t.Errorf("option flags = %+v", req)
	}
}

func TestMountFilePathImage(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	img := filepath.Join(work, "bare.sqsh")
	writeTestFile(t, img, "not really squashfs")
	view := filepath.Join(work, "view")

	stdout, stderr, _, err := runCmd(t, append(lib, "mount", img, view)...)
	if err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	if stdout != "mounted "+view+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	req := f.mountReqs[0]
	if req.Full != img || req.FullFSName != "" {
		t.Errorf("file-path stack = %+v", req)
	}
}

func TestMountReusePrintsAlreadyMounted(t *testing.T) {
	// Same-image half of the identity gate: the engine reports
	// reuse and the CLI prints the reuse verb with exit 0.
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	f.reused = true
	img := filepath.Join(work, "x.sqsh")
	writeTestFile(t, img, "bytes")
	view := filepath.Join(work, "view")

	stdout, stderr, _, err := runCmd(t, append(lib, "mount", img, view)...)
	if err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	if stdout != "already mounted "+view+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(f.mountReqs))
	}
	if code := ExitCode(err); code != 0 {
		t.Errorf("ExitCode = %d, want 0", code)
	}
}

func TestMountDifferentImageConflictsAndExitsNonZero(t *testing.T) {
	// Different-image half of the identity gate (P4): the engine
	// refuses to restack a live view over a different image and
	// the CLI must surface the conflict with a non-zero exit, never
	// the reuse verb.
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	img := filepath.Join(work, "x.sqsh")
	writeTestFile(t, img, "bytes")
	view := filepath.Join(work, "view")
	f.mountErr = errors.New(
		`workspace: ` + view + ` already mounts a different view ` +
			`(source "alice/app", upper "/runtime/up-1-2/upper"); ` +
			`refusing to restack it silently; ` +
			`unmount it first to serve a different image`)

	stdout, _, _, err := runCmd(t, append(lib, "mount", img, view)...)
	if code := ExitCode(err); code != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, code)
	}
	if !strings.Contains(err.Error(), "different view") {
		t.Errorf("error %q does not surface the conflict", err.Error())
	}
	if strings.Contains(stdout, "already mounted") {
		t.Errorf("a conflict printed the reuse verb: %q", stdout)
	}
	if len(f.mountReqs) != 1 {
		t.Errorf("engine calls = %d, want 1", len(f.mountReqs))
	}
}

func TestMountTableConversion(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	table := []mountinfo.Mount{{
		Point:     filepath.Join(work, "other"),
		FSType:    "fuse.fuse-overlayfs",
		Source:    "overlay",
		SuperOpts: "rw,relatime",
		Upper:     "/somewhere/upper",
		Lowers:    []string{"/somewhere/lower-0"},
	}}
	f := withMountSeams(t, table)
	writeTestFile(t, filepath.Join(work, "x.sqsh"), "bytes")
	view := filepath.Join(work, "view")

	if _, stderr, _, err := runCmd(t, append(lib, "mount", filepath.Join(work, "x.sqsh"), view)...); err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	entries := f.mountReqs[0].Table
	if len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	want := workspace.Entry{
		Point: table[0].Point, FSType: table[0].FSType, Source: table[0].Source,
		Upper: table[0].Upper, Lowers: table[0].Lowers,
	}
	if !entryEqual(entries[0], want) {
		t.Fatalf("entry = %+v, want %+v", entries[0], want)
	}
}

// entryEqual compares the convertible fields; Lowers is a slice.
func entryEqual(got, want workspace.Entry) bool {
	return got.Point == want.Point && got.FSType == want.FSType &&
		got.Source == want.Source && got.Upper == want.Upper &&
		strings.Join(got.Lowers, ":") == strings.Join(want.Lowers, ":")
}

func TestMountCacheCopiesAndKeepsIdentity(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	repoDir := buildLibrary(t, home, lib, imagesDir, "alice/app")
	cacheDir := filepath.Join(home, "lib", "cache")
	f := withMountSeams(t, nil)
	view := filepath.Join(work, "view")
	args := append(lib, "mount", "--cache", "--cache-dir", cacheDir, "alice/app", view)

	if _, stderr, _, err := runCmd(t, args...); err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	original := filepath.Join(repoDir, "image.sqsh")
	if len(f.cacheCalls) != 1 {
		t.Fatalf("cache calls = %v", f.cacheCalls)
	}
	call := f.cacheCalls[0]
	if call.dir != cacheDir || call.src != original {
		t.Errorf("cache call = %+v", call)
	}
	meta, err := image.Load(filepath.Join(repoDir, "meta.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if call.want != meta.SHA256 {
		t.Errorf("cache key = %q, want the recorded digest", call.want)
	}
	req := f.mountReqs[0]
	if req.Full != call.out || req.FullFSName != original {
		t.Errorf("cached stack = %s with fsname %q", req.Full, req.FullFSName)
	}
}

func TestMountUsageErrors(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	buildLibrary(t, home, lib, imagesDir, "alice/app")
	f := withMountSeams(t, nil)
	view := filepath.Join(work, "view")
	writeTestFile(t, filepath.Join(home, "x.sqsh"), "bytes")
	tests := []struct {
		name string
		args []string
	}{
		{name: "too few arguments", args: []string{"mount", "alice/app"}},
		{name: "too many arguments", args: []string{"mount", "alice/app", view, "extra"}},
		{name: "missing file", args: []string{"mount", "nope.sqsh", view}},
		{name: "library-shaped missing path", args: []string{"mount", "a/b/c", view}},
		{name: "unknown library", args: []string{"mount", "bob/missing", view}},
		{
			name: "target inside the reserved tree",
			args: []string{"mount", "alice/app", filepath.Join(home, ".tiler", "view")},
		},
		{
			name: "absolute subdir",
			args: []string{"mount", "--subdir", "/abs", "alice/app", view},
		},
		{
			name: "subdir escaping the image",
			args: []string{"mount", "--subdir", "../up", "alice/app", view},
		},
		{
			name: "verify on a file path",
			args: []string{"mount", "--verify", filepath.Join(home, "x.sqsh"), view},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f.mountReqs = nil
			_, _, _, err := runCmd(t, append(lib, tc.args...)...)
			if ExitCode(err) != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
			}
			if len(f.mountReqs) != 0 {
				t.Fatalf("the engine ran despite the usage error: %v", f.mountReqs)
			}
		})
	}
}

func TestMountRuntimeErrors(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	repoDir := buildLibrary(t, home, lib, imagesDir, "alice/app")
	f := withMountSeams(t, nil)
	view := filepath.Join(work, "view")

	// A tampered image fails the default quick check.
	imagePath := filepath.Join(repoDir, "image.sqsh")
	body, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imagePath, append(body, 'x'), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = runCmd(t, append(lib, "mount", "alice/app", view)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "does not match meta") {
		t.Fatalf("tampered image: ExitCode(%v) = %d", err, ExitCode(err))
	}
	if err := os.WriteFile(imagePath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	// Restoring the bytes leaves the mtime changed; --verify ignores
	// mtime and re-hashes, so it passes while the quick check fails.
	if err := os.Chtimes(imagePath, time.Now(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = runCmd(t, append(lib, "mount", "alice/app", view)...)
	if ExitCode(err) != 1 {
		t.Fatalf("stale mtime: ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	if _, stderr, _, err := runCmd(t, append(lib, "mount", "--verify", "alice/app", view)...); err != nil {
		t.Fatalf("verify: %v\n%s", err, stderr)
	}

	// A chain entry whose layer file is missing names the layer.
	addDanglingChain(t, repoDir)
	_, _, _, err = runCmd(t, append(lib, "mount", "alice/app", view)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "chain layer 0123456789abcdef") {
		t.Fatalf("dangling chain: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}

	// Engine failures stay runtime errors; an unavailable environment
	// gains the doctor pointer.
	bare := filepath.Join(home, "x.sqsh")
	writeTestFile(t, bare, "bytes")
	f.mountErr = errors.New("workspace: view already mounts a different view")
	if _, _, _, err := runCmd(t, append(lib, "mount", bare, view)...); ExitCode(err) != 1 {
		t.Fatalf("conflict: ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	f.mountErr = fmt.Errorf("%w: squashfuse_ll is missing", workspace.ErrUnavailable)
	_, _, _, err = runCmd(t, append(lib, "mount", bare, view)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "tiler doctor") {
		t.Fatalf("unavailable: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
}

// addDanglingChain appends a chain entry whose layer file does not
// exist, simulating a broken library.
func addDanglingChain(t *testing.T, repoDir string) {
	t.Helper()
	meta, err := image.Load(filepath.Join(repoDir, "meta.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const id = "0123456789abcdef"
	meta.Chain = append(meta.Chain, image.ChainEntry{
		ID: id, Parent: meta.ID, Size: 4096,
		SHA256:    strings.Repeat(id, 4),
		MtimeUnix: 1710000001,
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	})
	if err := image.Store(filepath.Join(repoDir, "meta.yaml"), meta); err != nil {
		t.Fatal(err)
	}
}

func TestMountChainRequestKeepsMemberOrder(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(home, "lib", "runtime", "up-1-2", "upper")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "base.txt"), "base")
	buildImage(t, lib, "alice/app", src)
	writeTestFile(t, filepath.Join(upper, "new.txt"), "change")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(merged)
	stdout, stderr, _, err := runCmd(t, append(lib, "build")...)
	if err != nil {
		t.Fatalf("incr build: %v\n%s", err, stderr)
	}
	incrID := buildOutput(t, stdout)

	cacheDir := filepath.Join(home, "lib", "cache")
	f := withMountSeams(t, nil)
	view := filepath.Join(work, "view")
	args := append(lib, "mount", "--cache", "--cache-dir", cacheDir, "alice/app", view)
	if _, stderr, _, err := runCmd(t, args...); err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	repoDir := filepath.Join(imagesDir, "alice", "app")
	fullOrig := filepath.Join(repoDir, "image.sqsh")
	layerOrig := filepath.Join(repoDir, "incr", incrID+".sqsh")
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d", len(f.mountReqs))
	}
	req := f.mountReqs[0]
	// Cached copies carry the bytes; the full image comes first and
	// the chain layer follows in resolved order.
	if req.Full != filepath.Join(cacheDir, "cached-image.sqsh") ||
		len(req.Layers) != 1 ||
		req.Layers[0] != filepath.Join(cacheDir, "cached-"+incrID+".sqsh") {
		t.Fatalf("cached stack = %s %v", req.Full, req.Layers)
	}
	// Identities stay index-aligned with the cached byte paths.
	if req.FullFSName != fullOrig || len(req.LayerFSNames) != 1 ||
		req.LayerFSNames[0] != layerOrig {
		t.Fatalf("identities = %s %v", req.FullFSName, req.LayerFSNames)
	}
	if len(f.cacheCalls) != 2 || f.cacheCalls[0].src != fullOrig ||
		f.cacheCalls[1].src != layerOrig {
		t.Fatalf("cache calls = %+v", f.cacheCalls)
	}
	if f.cacheCalls[0].want == "" || f.cacheCalls[1].want == "" {
		t.Errorf("library members must key the cache by their recorded digest: %+v",
			f.cacheCalls)
	}
}

func TestMountCorruptMetaIsRuntimeError(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	repoDir := buildLibrary(t, home, lib, imagesDir, "alice/app")
	withMountSeams(t, nil)
	view := filepath.Join(work, "view")
	if err := os.WriteFile(filepath.Join(repoDir, "meta.yaml"), []byte("kind: full\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := runCmd(t, append(lib, "mount", "alice/app", view)...)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	if !strings.Contains(err.Error(), "meta") {
		t.Fatalf("err = %v, want the validation text", err)
	}
}
