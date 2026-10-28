//go:build linux

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// nsEnv marks a test process already running inside a private user
// namespace, so it never re-executes itself again.
const nsEnv = "TILER_MOUNT_TEST_NS"

// requireMountTestEnv runs the test directly when the current
// process can mount and create device nodes. Without that privilege
// it re-executes this single test inside a private unprivileged user
// namespace when one is available, and skips otherwise.
func requireMountTestEnv(t *testing.T) {
	t.Helper()
	err := mountCapable()
	if err == nil || os.Getenv(nsEnv) == "1" {
		if err != nil {
			t.Skipf("mount test environment unavailable: %v", err)
		}
		return
	}
	if _, lookErr := exec.LookPath("unshare"); lookErr != nil {
		t.Skipf("mount test needs privilege (%v) and unshare is missing", lookErr)
	}
	self, selfErr := os.Executable()
	if selfErr != nil {
		t.Fatalf("cannot locate the test binary: %v", selfErr)
	}
	cmd := exec.Command("unshare", "-rm", "--propagation", "private",
		self, "-test.run", "^"+t.Name()+"$", "-test.v", "-test.timeout", "8m")
	cmd.Env = append(os.Environ(), nsEnv+"=1")
	out, runErr := cmd.CombinedOutput()
	t.Logf("user namespace run:\n%s", strings.TrimSpace(string(out)))
	if runErr != nil {
		t.Fatalf("mount test failed inside a user namespace: %v: %s", runErr, out)
	}
	t.Skip("mount test ran inside an unprivileged user namespace")
}

// mountCapable probes everything the acceptance tests need: Linux, a
// usable /dev/fuse, every external tool, and the privilege to create
// whiteout device nodes.
func mountCapable() error {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		return fmt.Errorf("/dev/fuse: %w", err)
	}
	tools := []string{
		"mksquashfs", "unsquashfs", "squashfuse_ll",
		"fuse-overlayfs", "fusermount3", "git",
	}
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s: %w", tool, err)
		}
	}
	probe := filepath.Join(os.TempDir(), fmt.Sprintf("tiler-cli-mknod-probe-%d", os.Getpid()))
	defer func() { _ = os.Remove(probe) }()
	if err := syscall.Mknod(probe, syscall.S_IFCHR|0o600, 0); err != nil {
		return fmt.Errorf("creating whiteout device nodes needs privilege: %w", err)
	}
	return nil
}

// failCmd runs one command expecting a failure and returns the error.
func failCmd(t *testing.T, args ...string) error {
	t.Helper()
	_, _, _, err := runCmd(t, args...)
	if err == nil {
		t.Fatalf("command %v succeeded, want failure", args)
	}
	return err
}

func mountCmd(t *testing.T, lib []string, args ...string) string {
	t.Helper()
	full := append(append([]string{}, lib...), "mount")
	return mustCmd(t, append(full, args...)...)
}

// requireCreatedMount checks mount stdout for the create line plus
// wantMountLine (for example "mounted /path\n").
func requireCreatedMount(t *testing.T, out, wantMountLine string) {
	t.Helper()
	name, rest, ok := parseNewSessionCreated(out)
	if !ok || !generatedSessionName.MatchString(name) || rest != wantMountLine {
		t.Fatalf("mount output = %q, want new session created: <name>\\n%s",
			out, wantMountLine)
	}
}

func umountCmd(t *testing.T, lib []string, dir string) string {
	t.Helper()
	full := append(append([]string{}, lib...), "umount")
	return mustCmd(t, append(full, dir)...)
}

// buildFullImage packs src into the library with the real toolchain.
func buildFullImage(t *testing.T, lib []string, name, src string) string {
	t.Helper()
	stdout := mustCmd(t, append(lib, "build", "--image", name, src)...)
	return buildOutput(t, stdout)
}

// buildIncrIn packs the writable upper of a live view: the command
// runs with the view as its working directory, exactly like a
// developer building inside the workspace.
func buildIncrIn(t *testing.T, lib []string, view, back string) string {
	t.Helper()
	t.Chdir(view)
	defer t.Chdir(back)
	stdout := mustCmd(t, append(lib, "build")...)
	return buildOutput(t, stdout)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "safe.directory=*"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// gitWorkSource is a small working repository with extra refs.
func gitWorkSource(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	files := map[string]string{
		"keep.txt":      "keep\n",
		"drop.txt":      "drop\n",
		"docs/guide.md": "# guide\n",
	}
	for rel, body := range files {
		writeTestFile(t, filepath.Join(src, rel), body)
	}
	runGit(t, src, "init", "-b", "main")
	runGit(t, src, "add", "-A")
	runGit(t, src, "-c", "user.name=test", "-c", "user.email=test@example.com",
		"commit", "-m", "initial import")
	runGit(t, src, "branch", "feature")
	runGit(t, src, "tag", "v1")
	// A pack keeps the build's multi-pack-index write working, the
	// same preparation the image round trip uses.
	runGit(t, src, "repack", "-d")
	return src
}

// subdirSource is a plain tree with a nested subtree for the subdir
// acceptance run.
func subdirSource(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	files := map[string]string{
		"top.txt":        "top\n",
		"sub/inner.txt":  "inner\n",
		"sub/gone.txt":   "gone\n",
		"sub/deep/x.txt": "x\n",
	}
	for rel, body := range files {
		writeTestFile(t, filepath.Join(src, rel), body)
	}
	return src
}

func fileSHA256Hex(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// assertBytesFile checks one text file the mounted views expose.
func assertBytesFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

// listTree folds a subtree into sorted "path: digest" lines so two
// views can be compared byte for byte.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		switch {
		case entry.IsDir():
			lines = append(lines, rel+"/")
		case entry.Type().IsRegular():
			lines = append(lines, rel+":"+fileSHA256Hex(t, path))
		default:
			lines = append(lines, rel+"?")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return lines
}

func realTable(t *testing.T) []mountinfo.Mount {
	t.Helper()
	table, err := mountinfo.Load()
	if err != nil {
		t.Fatal(err)
	}
	return table
}

func entryAt(table []mountinfo.Mount, point string) (mountinfo.Mount, bool) {
	found := mountinfo.Mount{}
	ok := false
	for _, m := range table {
		if m.Point == point {
			found, ok = m, true
		}
	}
	return found, ok
}

func entriesUnder(table []mountinfo.Mount, prefix string) []mountinfo.Mount {
	var out []mountinfo.Mount
	for _, m := range table {
		if strings.HasPrefix(m.Point, prefix+"/") {
			out = append(out, m)
		}
	}
	return out
}

// findDaemon returns the pid of the tool process whose command line
// contains the needle, failing the test when there is none.
func findDaemon(t *testing.T, tool, needle string) int {
	t.Helper()
	pid, ok := hasDaemon(tool, needle)
	if !ok {
		t.Fatalf("no %s daemon matching %q", tool, needle)
	}
	return pid
}

// hasDaemon returns the pid of the newest tool process whose command
// line contains the needle. The newest match wins so a daemon that is
// still exiting never shadows its replacement, the same rule the
// mount table enrichment uses.
func hasDaemon(tool, needle string) (int, bool) {
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	best := 0
	for _, proc := range procs {
		pid, convErr := strconv.Atoi(proc.Name())
		if convErr != nil || pid <= best {
			continue
		}
		data, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if readErr != nil {
			continue
		}
		parts := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
		if len(parts) == 0 || filepath.Base(parts[0]) != tool {
			continue
		}
		if strings.Contains(strings.Join(parts, " "), needle) {
			best = pid
		}
	}
	return best, best != 0
}

// deferForceUmount tears the test's mounts down best effort, so a
// failed assertion never leaks daemons or blocks temp-dir removal.
func deferForceUmount(t *testing.T, runtimeDir string, points ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, point := range points {
			_ = exec.Command("fusermount3", "-uz", point).Run()
		}
		table, err := mountinfo.Load()
		if err != nil {
			return
		}
		stray := entriesUnder(table, runtimeDir)
		for i := len(stray) - 1; i >= 0; i-- {
			_ = exec.Command("fusermount3", "-uz", stray[i].Point).Run()
		}
	})
}

// waitStale polls until stat of the point fails, which is how a
// killed FUSE daemon shows up before cleanup.
func waitStale(t *testing.T, point string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(point); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never went stale", point)
}

// waitOverlayDead polls until the merged point reports a dead FUSE
// connection, goes stale, or its table entry vanished. A killed
// daemon shows up in at least one of the three: the kernel attribute
// cache can keep stat answering after the connection died, so the
// open probe is the reliable signal.
func waitOverlayDead(t *testing.T, point string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(point); err != nil {
			return
		}
		if err := openProbe(point); err != nil {
			return
		}
		if _, listed := entryAt(realTable(t), point); !listed {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s kept a healthy overlay entry", point)
}

// openProbe forces one request through the mount; a dead connection
// answers with ENOTCONN while stat can keep succeeding from cache.
func openProbe(point string) error {
	file, err := os.Open(point)
	if err != nil {
		return err
	}
	_ = file.Close()
	return nil
}

func TestMountReadOnlyCloneFromBareImage(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	bare := filepath.Join(t.TempDir(), "repo.git")
	runGit(t, work, "clone", "--bare", gitWorkSource(t), bare)
	id := buildFullImage(t, lib, "alice/app", bare)
	imagePath := filepath.Join(imagesDir, "alice", "app", "_objects", id, "image.sqsh")
	before := fileSHA256Hex(t, imagePath)

	view := filepath.Join(work, "view")
	deferForceUmount(t, sessionsRoot, view)
	requireCreatedMount(t, mountCmd(t, lib, "--read-only", "alice/app", view),
		"mounted "+view+"\n")

	clone := filepath.Join(work, "clone")
	runGit(t, work, "clone", view, clone)
	runGit(t, clone, "fsck")
	runGit(t, clone, "-c", "user.name=test", "-c", "user.email=test@example.com",
		"commit", "--allow-empty", "-m", "independent work")
	if out := runGit(t, clone, "rev-parse", "HEAD"); strings.TrimSpace(out) == "" {
		t.Fatal("the clone has no HEAD")
	}
	if out := runGit(t, view, "rev-parse", "--is-bare-repository"); strings.TrimSpace(out) != "true" {
		t.Fatalf("mounted view is not a bare repository: %q", out)
	}

	if after := fileSHA256Hex(t, imagePath); after != before {
		t.Errorf("image digest changed: %s -> %s", before, after)
	}
	info, err := os.Stat(view)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) {
		t.Errorf("mount point owner = %+v, want euid %d", info.Sys(), os.Geteuid())
	}
	if err := os.WriteFile(filepath.Join(view, "nope.txt"), []byte("x"), 0o644); err == nil {
		t.Error("a read-only view accepted a write")
	}
	if out := umountCmd(t, lib, view); out != "unmounted "+view+"\n" {
		t.Fatalf("umount output = %q", out)
	}
}

func TestMountReadWriteUpperIsolation(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	id := buildFullImage(t, lib, "alice/app", gitWorkSource(t))
	imagePath := filepath.Join(imagesDir, "alice", "app", "_objects", id, "image.sqsh")
	before := fileSHA256Hex(t, imagePath)

	view := filepath.Join(work, "view")
	deferForceUmount(t, sessionsRoot, view)
	requireCreatedMount(t, mountCmd(t, lib, "alice/app", view), "mounted "+view+"\n")
	entry, ok := entryAt(realTable(t), view)
	if !ok || entry.Upper == "" {
		t.Fatalf("no writable overlay entry at %s: %+v", view, entry)
	}
	writeTestFile(t, filepath.Join(view, "extra.txt"), "extra\n")
	writeTestFile(t, filepath.Join(view, "keep.txt"), "changed\n")

	assertBytesFile(t, filepath.Join(entry.Upper, "extra.txt"), "extra\n")
	assertBytesFile(t, filepath.Join(entry.Upper, "keep.txt"), "changed\n")
	if after := fileSHA256Hex(t, imagePath); after != before {
		t.Errorf("lower image digest changed: %s -> %s", before, after)
	}

	// A repeated mount is an idempotent reuse of the same view and
	// does not announce a discarded generated session.
	if out := mountCmd(t, lib, "alice/app", view); out != "already mounted "+view+"\n" {
		t.Fatalf("remount output = %q, want already mounted without a create line", out)
	}
	assertBytesFile(t, filepath.Join(view, "extra.txt"), "extra\n")

	if out := umountCmd(t, lib, view); out != "unmounted "+view+"\n" {
		t.Fatalf("umount output = %q", out)
	}
	if out := umountCmd(t, lib, view); out != "" {
		t.Fatalf("an idempotent umount printed %q", out)
	}

	// A remount into a fresh generated session gets an empty upper:
	// writable layers are session-scoped, not mountpoint-scoped.
	requireCreatedMount(t, mountCmd(t, lib, "alice/app", view), "mounted "+view+"\n")
	if _, err := os.Stat(filepath.Join(view, "extra.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh session upper leaked prior writes: %v", err)
	}
	assertBytesFile(t, filepath.Join(view, "keep.txt"), "keep\n")
	if out := umountCmd(t, lib, view); out != "unmounted "+view+"\n" {
		t.Fatalf("final umount output = %q", out)
	}
}

func TestMountCascadingChain(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	buildFullImage(t, lib, "alice/app", gitWorkSource(t))

	view1 := filepath.Join(work, "view1")
	view2 := filepath.Join(work, "view2")
	view3 := filepath.Join(work, "view3")
	deferForceUmount(t, sessionsRoot, view1, view2, view3)

	mountCmd(t, lib, "alice/app", view1)
	writeTestFile(t, filepath.Join(view1, "new.txt"), "new\n")
	writeTestFile(t, filepath.Join(view1, "keep.txt"), "changed\n")
	if err := os.Remove(filepath.Join(view1, "drop.txt")); err != nil {
		t.Fatal(err)
	}
	runGit(t, view1, "branch", "added-branch")
	runGit(t, view1, "branch", "-D", "feature")
	runGit(t, view1, "tag", "-d", "v1")
	layer1 := buildIncrIn(t, lib, view1, work)
	umountCmd(t, lib, view1)

	mountCmd(t, lib, "alice/app", view2)
	writeTestFile(t, filepath.Join(view2, "second.txt"), "second\n")
	buildIncrIn(t, lib, view2, work)
	umountCmd(t, lib, view2)

	// The read-only cascade merges the full image plus both layers.
	requireCreatedMount(t, mountCmd(t, lib, "--read-only", "alice/app", view3),
		"mounted "+view3+"\n")
	assertBytesFile(t, filepath.Join(view3, "new.txt"), "new\n")
	assertBytesFile(t, filepath.Join(view3, "second.txt"), "second\n")
	assertBytesFile(t, filepath.Join(view3, "keep.txt"), "changed\n")
	if _, err := os.Stat(filepath.Join(view3, "drop.txt")); err == nil {
		t.Error("a deleted file came back in the cascade")
	}
	refs := runGit(t, view3, "for-each-ref", "--format=%(refname)")
	for _, want := range []string{"refs/heads/main", "refs/heads/added-branch"} {
		if !strings.Contains(refs, want) {
			t.Errorf("ref %s missing:\n%s", want, refs)
		}
	}
	for _, gone := range []string{"refs/heads/feature", "refs/tags/v1"} {
		if strings.Contains(refs, gone) {
			t.Errorf("deleted ref %s came back:\n%s", gone, refs)
		}
	}
	umountCmd(t, lib, view3)

	// A missing chain member file fails the mount with a clear error.
	layerPath := filepath.Join(imagesDir, "alice", "app", "_objects", layer1, "image.sqsh")
	if err := os.Remove(layerPath); err != nil {
		t.Fatal(err)
	}
	view4 := filepath.Join(work, "view4")
	err := failCmd(t, append(lib, "mount", "alice/app", view4)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "image "+layer1) {
		t.Fatalf("broken chain: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
}

func TestMountSubdirRouting(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	buildFullImage(t, lib, "alice/app", subdirSource(t))

	view := filepath.Join(work, "sub-view")
	missing := filepath.Join(work, "missing-sub")
	viewA := filepath.Join(work, "chain-sub")
	viewB := filepath.Join(work, "chain-full")
	rwView := filepath.Join(work, "rw")
	deferForceUmount(t, sessionsRoot, view, viewA, viewB, rwView)

	requireCreatedMount(t,
		mountCmd(t, lib, "--read-only", "--subdir", "sub", "alice/app", view),
		"mounted "+view+"\n")
	assertBytesFile(t, filepath.Join(view, "inner.txt"), "inner\n")
	assertBytesFile(t, filepath.Join(view, "deep", "x.txt"), "x\n")
	if _, err := os.Stat(filepath.Join(view, "top.txt")); err == nil {
		t.Error("the image root leaked into a subdir mount")
	}
	// ".." from the mount root lands on the host parent directory.
	upInfo, err := os.Stat(filepath.Join(view, ".."))
	if err != nil {
		t.Fatal(err)
	}
	hostInfo, err := os.Stat(work)
	if err != nil {
		t.Fatal(err)
	}
	upSt, upOK := upInfo.Sys().(*syscall.Stat_t)
	hostSt, hostOK := hostInfo.Sys().(*syscall.Stat_t)
	if !upOK || !hostOK || upSt.Dev != hostSt.Dev || upSt.Ino != hostSt.Ino {
		t.Error(`".." inside a subdir mount is not the host directory`)
	}
	if err := os.WriteFile(filepath.Join(view, "nope.txt"), []byte("x"), 0o644); err == nil {
		t.Error("a read-only subdir view accepted a write")
	}
	umountCmd(t, lib, view)

	// A subdir no member contains is refused before any mount.
	err = failCmd(t, append(lib, "mount", "--subdir", "nope", "alice/app", missing)...)
	if ExitCode(err) != 1 || !strings.Contains(err.Error(), "subdir nope") {
		t.Fatalf("missing subdir: ExitCode(%v) = %d: %v", err, ExitCode(err), err)
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Error("the refused mount left the target directory behind")
	}
	if _, listed := entryAt(realTable(t), missing); listed {
		t.Error("the refused mount left a mount table entry")
	}
	if _, found := hasDaemon("squashfuse_ll", missing); found {
		t.Error("the refused mount left a daemon behind")
	}

	// One layer touching the subtree, then the unified subdir view
	// must match the subtree of the full expansion, whiteouts and
	// all.
	mountCmd(t, lib, "alice/app", rwView)
	if rmErr := os.Remove(filepath.Join(rwView, "sub", "gone.txt")); rmErr != nil {
		t.Fatal(rmErr)
	}
	writeTestFile(t, filepath.Join(rwView, "sub", "fresh.txt"), "fresh\n")
	buildIncrIn(t, lib, rwView, work)
	umountCmd(t, lib, rwView)

	mountCmd(t, lib, "--read-only", "--subdir", "sub", "alice/app", viewA)
	mountCmd(t, lib, "--read-only", "alice/app", viewB)
	subTree := listTree(t, viewA)
	fullSubTree := listTree(t, filepath.Join(viewB, "sub"))
	if !slices.Equal(subTree, fullSubTree) {
		t.Errorf("subdir view differs from the full expansion subtree:\n%v\n%v",
			subTree, fullSubTree)
	}
	joined := strings.Join(subTree, "\n")
	if strings.Contains(joined, "gone.txt") {
		t.Errorf("the whiteout did not take effect in the subtree:\n%s", joined)
	}
	if !strings.Contains(joined, "fresh.txt") {
		t.Errorf("the layer addition is missing from the subtree:\n%s", joined)
	}
	umountCmd(t, lib, viewA)
	umountCmd(t, lib, viewB)
}

func TestMountCacheKeepsTableIdentity(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	id := buildFullImage(t, lib, "alice/app", subdirSource(t))
	imagePath := filepath.Join(imagesDir, "alice", "app", "_objects", id, "image.sqsh")
	digest := fileSHA256Hex(t, imagePath)
	cacheDir := filepath.Join(home, "lib", "cache")

	cached := filepath.Join(work, "cached-view")
	plain := filepath.Join(work, "plain-view")
	deferForceUmount(t, sessionsRoot, cached, plain)

	requireCreatedMount(t,
		mountCmd(t, lib, "--cache", "--cache-dir", cacheDir, "alice/app", cached),
		"mounted "+cached+"\n")
	cachedCopy := filepath.Join(cacheDir, digest+".sqsh")
	if _, err := os.Stat(cachedCopy); err != nil {
		t.Fatalf("the digest-named cache copy is missing: %v", err)
	}
	assertBytesFile(t, filepath.Join(cached, "top.txt"), "top\n")
	// The daemon serves the cached copy bytes.
	if _, found := hasDaemon("squashfuse_ll", cachedCopy); !found {
		t.Error("no daemon mounts the cached copy")
	}
	umountCmd(t, lib, cached)

	// With the cache disabled nothing is copied and the daemon
	// serves the original bytes.
	entriesBefore, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	requireCreatedMount(t, mountCmd(t, lib, "alice/app", plain), "mounted "+plain+"\n")
	entriesAfter, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		t.Errorf("a disabled cache grew: %d -> %d entries", len(entriesBefore), len(entriesAfter))
	}
	if _, found := hasDaemon("squashfuse_ll", imagePath); !found {
		t.Error("no daemon mounts the original image")
	}
	umountCmd(t, lib, plain)
}

func TestMountStaleTeardownAndRemount(t *testing.T) {
	requireMountTestEnv(t)
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessionsRoot := sessionsDir(home)
	src := subdirSource(t)
	buildFullImage(t, lib, "alice/app", src)

	view := filepath.Join(work, "view")
	deferForceUmount(t, sessionsRoot, view)
	mountCmd(t, lib, "alice/app", view)
	entry, ok := entryAt(realTable(t), view)
	if !ok || len(entry.Lowers) == 0 {
		t.Fatalf("no overlay stack at %s: %+v", view, entry)
	}
	lower := entry.Lowers[0]

	// Kill the squashfuse daemon: the lower stays listed but dead,
	// and umount must recover through the fallback unmount.
	killDaemon(t, findDaemon(t, "squashfuse_ll", lower))
	waitStale(t, lower)
	if out := umountCmd(t, lib, view); out != "unmounted "+view+"\n" {
		t.Fatalf("umount output = %q", out)
	}
	assertNoResidue(t, sessionsRoot, view)

	// A fresh mount over the same target works again.
	mountCmd(t, lib, "alice/app", view)
	assertBytesFile(t, filepath.Join(view, "top.txt"), "top\n")

	// Kill the overlay daemon: the merged entry goes stale or
	// vanishes; remounting over it must clean up and succeed.
	killDaemon(t, findDaemon(t, "fuse-overlayfs", view))
	waitOverlayDead(t, view)
	requireCreatedMount(t, mountCmd(t, lib, "alice/app", view), "mounted "+view+"\n")
	assertBytesFile(t, filepath.Join(view, "top.txt"), "top\n")
	if out := umountCmd(t, lib, view); out != "unmounted "+view+"\n" {
		t.Fatalf("final umount output = %q", out)
	}
	assertNoResidue(t, sessionsRoot, view)
}

// killDaemon sends SIGKILL and waits for the process to disappear.
func killDaemon(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon %d survived SIGKILL", pid)
}

// assertNoResidue checks the target is a plain directory and the
// mount table lists nothing at it or under the runtime tree.
func assertNoResidue(t *testing.T, runtimeDir, view string) {
	t.Helper()
	table := realTable(t)
	if _, listed := entryAt(table, view); listed {
		t.Errorf("the mount table still lists %s", view)
	}
	if stray := entriesUnder(table, runtimeDir); len(stray) != 0 {
		t.Errorf("runtime residue remains: %+v", stray)
	}
	info, err := os.Stat(view)
	if err != nil {
		t.Fatalf("target is not statable after teardown: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("target is not a directory after teardown: %v", info.Mode())
	}
	entries, err := os.ReadDir(view)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("target is not a plain directory after teardown: %v", names)
	}
}
