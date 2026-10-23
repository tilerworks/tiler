package cli

import (
	"crypto/sha256"
	"encoding/hex"
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
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	// The reassembled file has to carry squashfs magic so the
	// library consistency check can see the container the fixture
	// metadata records.
	if err := os.WriteFile(out, []byte("hsqs-reassembled"), 0o644); err != nil {
		return "", err
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
	origAssemble := assembleBlocks
	t.Cleanup(func() {
		mountRun, ensureCache, loadMountTable = origRun, origCache, origTable
		mountEngine, unmountEngine = origMount, origUmount
		assembleBlocks = origAssemble
	})
	mountRun = f.runTool
	ensureCache = f.cache
	loadMountTable = func() ([]mountinfo.Mount, error) { return f.table, nil }
	mountEngine = f.mount
	unmountEngine = f.umount
	assembleBlocks = f.assemble
	installFoundTools(t)
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
		// Command tests drive the mount table. The darwin reader
		// would otherwise reconcile through a live hdiutil info.
		engine.LoadDiskImages = nil
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
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	return repo.Dir
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
	name, rest, ok := parseNewSessionCreated(stdout)
	if !ok || !generatedSessionName.MatchString(name) || rest != "mounted "+view+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d", len(f.mountReqs))
	}
	req := f.mountReqs[0]
	if req.Full != filepath.Join(repoDir, "image.sqsh") || len(req.Layers) != 0 {
		t.Errorf("stack = %s %v", req.Full, req.Layers)
	}
	if req.Format != image.FormatSquashfs {
		t.Errorf("format = %q, want squashfs", req.Format)
	}
	if req.ReadOnly || req.Subdir != "" || req.FullFSName != "" {
		t.Errorf("flags leaked: %+v", req)
	}
	if req.MergedDir != view {
		t.Errorf("merged = %s, want %s", req.MergedDir, view)
	}
	// Generated session name varies; require sessions/<name>/runtime/m1.
	wantRuntime := filepath.Join(sessionsDir(home), name, "runtime", "m1")
	if req.RuntimeDir != wantRuntime {
		t.Errorf("runtime = %s, want %s", req.RuntimeDir, wantRuntime)
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
	writeSquashFile(t, img)
	view := filepath.Join(work, "view")

	stdout, stderr, _, err := runCmd(t, append(lib, "mount", img, view)...)
	if err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	name, rest, ok := parseNewSessionCreated(stdout)
	if !ok || !generatedSessionName.MatchString(name) || rest != "mounted "+view+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	req := f.mountReqs[0]
	if req.Full != img || req.FullFSName != "" || req.Format != image.FormatSquashfs {
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
	writeSquashFile(t, img)
	view := filepath.Join(work, "view")

	stdout, stderr, _, err := runCmd(t, append(lib, "mount", img, view)...)
	if err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	if stdout != "already mounted "+view+"\n" {
		t.Fatalf("stdout = %q, want already mounted without a create line", stdout)
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
	writeSquashFile(t, img)
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
	if strings.Contains(stdout, "new session created:") {
		t.Errorf("a conflict announced a discarded session: %q", stdout)
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
	writeSquashFile(t, filepath.Join(work, "x.sqsh"))
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
			name: "target covering the sessions tree",
			args: []string{"mount", "alice/app", filepath.Join(home, "lib")},
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
	writeSquashFile(t, bare)
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
	layerOrig := filepath.Join(repoDir, "_incr", incrID+".sqsh")
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

func TestMountFilePathDetectsErofs(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	img := filepath.Join(work, "layer.img")
	writeErofsFile(t, img)
	view := filepath.Join(work, "view")

	if _, stderr, _, err := runCmd(t, append(lib, "mount", img, view)...); err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 || f.mountReqs[0].Format != image.FormatErofs {
		t.Fatalf("request = %+v, want erofs", f.mountReqs)
	}
}

func TestMountFilePathRefusesUnknownMagic(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	img := filepath.Join(work, "plain.img")
	writeTestFile(t, img, "not a container")
	view := filepath.Join(work, "view")

	_, _, _, err := runCmd(t, append(lib, "mount", img, view)...)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "not detected") {
		t.Fatalf("ExitCode(%v) = %d, want a usage refusal", err, ExitCode(err))
	}
	if len(f.mountReqs) != 0 {
		t.Fatalf("the engine ran: %v", f.mountReqs)
	}
}

func TestMountRefusesMetaMagicMismatch(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	body := make([]byte, 1028)
	copy(body[1024:], []byte{0xE2, 0xE1, 0xF5, 0xE0})
	writeLibraryContainer(t, imagesDir, image.FormatSquashfs, body)
	view := filepath.Join(work, "view")

	_, _, _, err := runCmd(t, append(lib, "mount", "alice/app", view)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "erofs") ||
		!strings.Contains(err.Error(), "squashfs") {
		t.Fatalf("ExitCode(%v) = %d, want a meta/magic refusal", err, ExitCode(err))
	}
	if len(f.mountReqs) != 0 {
		t.Fatalf("the engine ran: %v", f.mountReqs)
	}
}

// TestMountFormatRefusalLeavesNamedSessionEmpty checks that a
// deterministic format refusal never becomes a replayable mount
// action. Implicit sessions used to hide the write-ahead record by
// discarding it; a named session keeps every failed action.
func TestMountFormatRefusalLeavesNamedSessionEmpty(t *testing.T) {
	t.Run("unknown magic file", func(t *testing.T) {
		home, work := withWorkspace(t)
		_, lib := libFlags(home)
		f := withMountSeams(t, nil)
		mustSessionNew(t, lib, "dev")
		img := filepath.Join(work, "notes.txt")
		writeTestFile(t, img, "not a container")
		view := filepath.Join(work, "view")

		_, _, _, err := runCmd(t, append(lib, "mount", "--session", "dev", img, view)...)
		if ExitCode(err) != 2 || !strings.Contains(err.Error(), "not detected") {
			t.Fatalf("ExitCode(%v) = %d, want a usage refusal", err, ExitCode(err))
		}
		assertNoMountAction(t, f, home, "dev")
	})

	t.Run("meta magic mismatch", func(t *testing.T) {
		home, work := withWorkspace(t)
		imagesDir, lib := libFlags(home)
		f := withMountSeams(t, nil)
		mustSessionNew(t, lib, "dev")
		body := make([]byte, 1028)
		copy(body[1024:], []byte{0xE2, 0xE1, 0xF5, 0xE0})
		writeLibraryContainer(t, imagesDir, image.FormatSquashfs, body)
		view := filepath.Join(work, "view")

		_, _, _, err := runCmd(t, append(lib, "mount", "--session", "dev", "alice/app", view)...)
		if ExitCode(err) != 1 || !strings.Contains(err.Error(), "erofs") ||
			!strings.Contains(err.Error(), "squashfs") {
			t.Fatalf("ExitCode(%v) = %d, want a meta/magic refusal", err, ExitCode(err))
		}
		assertNoMountAction(t, f, home, "dev")
	})

	t.Run("blocks meta magic mismatch", func(t *testing.T) {
		home, work := withWorkspace(t)
		imagesDir, lib := libFlags(home)
		f := withMountSeams(t, nil)
		mustSessionNew(t, lib, "dev")
		writeBlocksFixture(t, imagesDir, "alice/app", []string{"block-one-bytes", "tail"}, true)
		repo := filepath.Join(imagesDir, "alice", "app")
		meta, err := image.Load(filepath.Join(repo, "meta.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		meta.Format.Type = image.FormatErofs
		storeFixtureMeta(t, repo, meta)
		view := filepath.Join(work, "view")

		_, _, _, err = runCmd(t, append(lib, "mount", "--session", "dev", "alice/app", view)...)
		if ExitCode(err) != 1 || !strings.Contains(err.Error(), "erofs") ||
			!strings.Contains(err.Error(), "squashfs") {
			t.Fatalf("ExitCode(%v) = %d, want a meta/magic refusal after reassembly", err, ExitCode(err))
		}
		if f.assembleN != 1 {
			t.Fatalf("assemble calls = %d, want one reassembly before the refusal", f.assembleN)
		}
		assertNoMountAction(t, f, home, "dev")
	})
}

// assertNoMountAction reports when a format refusal still invoked
// the engine or left a mount action on the named session.
func assertNoMountAction(t *testing.T, f *mountFake, home, name string) {
	t.Helper()
	if len(f.mountReqs) != 0 {
		t.Fatalf("the engine ran: %v", f.mountReqs)
	}
	if rec := loadSessionRecord(t, sessionsDir(home), name); len(rec.Actions) != 0 {
		t.Fatalf("session %s recorded a mount action: %+v", name, rec.Actions)
	}
}

func TestMountLibraryErofsReachesTheEngine(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	body := make([]byte, 1028)
	copy(body[1024:], []byte{0xE2, 0xE1, 0xF5, 0xE0})
	writeLibraryContainer(t, imagesDir, image.FormatErofs, body)
	view := filepath.Join(work, "view")

	if _, stderr, _, err := runCmd(t, append(lib, "mount", "alice/app", view)...); err != nil {
		t.Fatalf("mount: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 || f.mountReqs[0].Format != image.FormatErofs {
		t.Fatalf("request = %+v, want erofs", f.mountReqs)
	}
}

// writeLibraryContainer publishes one single-file library image whose
// bytes and metadata agree on size and mtime. format is the type
// recorded in metadata; body is the file the magic check reads.
func writeLibraryContainer(t *testing.T, imagesDir, format string, body []byte) {
	t.Helper()
	repo := filepath.Join(imagesDir, "alice", "app")
	path := filepath.Join(repo, image.ImageFile)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1710000000, 0).UTC()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	meta := image.Meta{
		ID: image.ID(sum), Name: "alice/app",
		Kind:      image.KindFull,
		Format:    image.Format{Type: format, Compression: image.CompressionZstd},
		Size:      int64(len(body)),
		SHA256:    hex.EncodeToString(sum[:]),
		MtimeUnix: when.Unix(),
		CreatedAt: when,
		Builder:   "tiler test",
	}
	if err := image.Store(filepath.Join(repo, "meta.yaml"), meta); err != nil {
		t.Fatal(err)
	}
}

func TestMountRefusesWhenNoStack(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	toolLookPath = func(string) (string, error) { return "", errors.New("not found") }
	view := filepath.Join(work, "view")
	_, _, _, err := runCmd(t, append(lib, "mount", "alice/app", view)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "no complete image toolchain") ||
		!strings.Contains(err.Error(), "tiler doctor") {
		t.Fatalf("ExitCode(%v) = %d", err, ExitCode(err))
	}
}

func TestMountRefusesIncompleteFormatTools(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	f := withMountSeams(t, nil)
	writeLibraryContainer(t, imagesDir, image.FormatSquashfs, []byte("hsqs-fake-image"))
	toolLookPath = lookOnly("mkfs.erofs", "erofsfuse")
	view := filepath.Join(work, "view")
	if err := os.Mkdir(view, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := runCmd(t, append(lib, "mount", "alice/app", view)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "squashfs toolchain is incomplete") {
		t.Fatalf("ExitCode(%v) = %d", err, ExitCode(err))
	}
	if len(f.mountReqs) != 0 {
		t.Fatalf("engine calls = %d, want none", len(f.mountReqs))
	}
}
