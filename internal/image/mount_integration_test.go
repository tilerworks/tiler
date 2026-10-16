//go:build linux

package image

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/workspace"
)

// nsEnv marks a test process already running inside a private user
// namespace, so it never re-executes itself again.
const nsEnv = "TILER_MOUNT_TEST_NS"

// TestIncrementalWorkspaceRoundTrip is the incremental fidelity
// blueprint: build a full image from a git source, change files and
// refs through a writable view, pack the upper, and verify the
// rebuilt view matches the live tree byte for byte, twice over.
func TestIncrementalWorkspaceRoundTrip(t *testing.T) {
	requireMountTestEnv(t)
	cfg := config.Defaults(t.TempDir())
	src := gitSource(t)
	full, err := BuildFull(FullRequest{
		Config: cfg, Name: "alice/app", SrcDir: src,
		Version: "v1", Builder: "test", Source: Source{Ref: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}

	view1 := filepath.Join(t.TempDir(), "view1")
	w1, err := mountWorkspace(t, cfg, full.Path, nil, view1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w1.Unmount() }()
	assertBytes(t, filepath.Join(view1, "keep.txt"), []byte("keep\n"))

	// Add, change, and delete files and refs through the live view.
	if err := os.WriteFile(filepath.Join(view1, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view1, "keep.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(view1, "drop.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(view1, "opqdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(view1, "opqdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view1, "opqdir", "fresh.txt"), []byte("fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, view1, "branch", "added-branch")
	runGit(t, view1, "branch", "-D", "feature")
	runGit(t, view1, "tag", "-d", "v1")

	assertUpperMarkers(t, w1.UpperDir)
	live1 := treeDigest(t, view1)
	refs1 := gitRefs(t, view1)
	assertRefSet(t, refs1, "refs/heads/added-branch", "refs/heads/main")
	changedBytes := dirBytes(t, w1.UpperDir)

	// Pack the upper. MountSmoke stays nil, so the default gate
	// really mounts the fresh layer read-only.
	inc1, err := BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: w1.UpperDir,
		Source: Source{Ref: "added-branch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertLayerSizeBound(t, inc1.Path, changedBytes)
	if err := w1.Unmount(); err != nil {
		t.Fatal(err)
	}

	view2 := filepath.Join(t.TempDir(), "view2")
	w2, err := mountWorkspace(t, cfg, full.Path, []string{inc1.Path}, view2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w2.Unmount() }()
	if got := treeDigest(t, view2); got != live1 {
		t.Errorf("rebuilt view digest does not match the live tree")
	}
	refs2 := gitRefs(t, view2)
	if strings.Join(refs2, "\n") != strings.Join(refs1, "\n") {
		t.Errorf("ref set changed across the rebuild:\n%v\n%v", refs1, refs2)
	}
	entries, err := os.ReadDir(filepath.Join(view2, "opqdir"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "fresh.txt" {
		t.Errorf("opaque directory leaked lower entries: %v", entries)
	}

	// A second layer on top of the rebuilt view.
	if err := os.WriteFile(filepath.Join(view2, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(view2, "new.txt")); err != nil {
		t.Fatal(err)
	}
	live2 := treeDigest(t, view2)
	inc2, err := BuildIncr(IncrRequest{
		Config: cfg, Name: "alice/app", Upper: w2.UpperDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Chain) != 2 || loaded.Chain[0].ID != inc1.ID ||
		loaded.Chain[1].ID != inc2.ID || loaded.Chain[1].Parent != inc1.ID {
		t.Fatalf("chain = %+v", loaded.Chain)
	}
	if err := w2.Unmount(); err != nil {
		t.Fatal(err)
	}

	view3 := filepath.Join(t.TempDir(), "view3")
	w3, err := mountWorkspace(t, cfg, full.Path, []string{inc1.Path, inc2.Path}, view3)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w3.Unmount() }()
	if got := treeDigest(t, view3); got != live2 {
		t.Errorf("two chained layers do not match expanding each layer")
	}
}

// mountWorkspace mounts one view and skips the test when mounting is
// unavailable on this host.
func mountWorkspace(t *testing.T, cfg config.Config, full string, layers []string, merged string) (*workspace.Workspace, error) {
	t.Helper()
	ws, err := workspace.Mount(workspace.Request{
		Full: full, Layers: layers,
		RuntimeDir: cfg.RuntimeDir, MergedDir: merged,
	})
	if errors.Is(err, workspace.ErrUnavailable) {
		t.Skipf("mounting unavailable: %v", err)
	}
	return ws, err
}

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
		t.Skipf("mount test needs privilege (%v) and unshare is missing", err)
	}
	self, selfErr := os.Executable()
	if selfErr != nil {
		t.Fatalf("cannot locate the test binary: %v", selfErr)
	}
	cmd := exec.Command("unshare", "-rm", "--propagation", "private",
		self, "-test.run", "^"+t.Name()+"$", "-test.v", "-test.timeout", "8m")
	cmd.Env = append(os.Environ(), nsEnv+"=1")
	out, runErr := cmd.CombinedOutput()
	t.Logf("user namespace run:\n%s", bytes.TrimSpace(out))
	if runErr != nil {
		t.Fatalf("mount test failed inside a user namespace: %v", runErr)
	}
	t.Skip("mount test ran inside an unprivileged user namespace")
}

// mountCapable probes everything the round trip needs: Linux, a
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
	probe := filepath.Join(os.TempDir(), fmt.Sprintf("tiler-mknod-probe-%d", os.Getpid()))
	defer func() { _ = os.Remove(probe) }()
	if err := syscall.Mknod(probe, syscall.S_IFCHR|0o600, 0); err != nil {
		return fmt.Errorf("creating whiteout device nodes needs privilege: %w", err)
	}
	return nil
}

// gitSource lays out a small repository with files and extra refs.
func gitSource(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	files := map[string]string{
		"keep.txt":      "keep\n",
		"drop.txt":      "drop\n",
		"docs/guide.md": "# guide\n",
		"opqdir/a.txt":  "a\n",
		"opqdir/b.txt":  "b\n",
	}
	for rel, body := range files {
		path := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, src, "init", "-b", "main")
	runGit(t, src, "add", "-A")
	runGit(t, src, "-c", "user.name=test", "-c", "user.email=test@example.com",
		"commit", "-m", "initial import")
	runGit(t, src, "branch", "feature")
	runGit(t, src, "tag", "v1")
	// A pack keeps multi-pack-index writing working, matching the
	// mirror clones callers prepare.
	runGit(t, src, "repack", "-d")
	return src
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// assertUpperMarkers checks the overlay markers in the upper: the
// deleted file is a character device 0:0 whiteout, and the recreated
// directory is opaque through both the xattr and the marker file.
func assertUpperMarkers(t *testing.T, upper string) {
	t.Helper()
	path := filepath.Join(upper, "drop.txt")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("whiteout missing: %v", err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		t.Fatalf("whiteout %s is not a character device: %v", path, info.Mode())
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Rdev != 0 {
		t.Fatalf("whiteout rdev is not 0:0: %+v", info.Sys())
	}
	opq := filepath.Join(upper, "opqdir")
	value := make([]byte, 8)
	n, err := syscall.Getxattr(opq, "user.fuseoverlayfs.opaque", value)
	if err != nil || n == 0 {
		t.Fatalf("opaque xattr missing on %s: %v", opq, err)
	}
	if _, err := os.Lstat(filepath.Join(opq, ".wh..wh..opq")); err != nil {
		t.Fatalf("opaque marker file missing: %v", err)
	}
}

// assertLayerSizeBound keeps the layer in the same order of
// magnitude as the changed bytes instead of an exact size.
func assertLayerSizeBound(t *testing.T, layer string, changedBytes int64) {
	t.Helper()
	info, err := os.Stat(layer)
	if err != nil {
		t.Fatal(err)
	}
	limit := 4*changedBytes + 256*1024
	if info.Size() > limit {
		t.Errorf("layer size %d exceeds the bound %d for %d changed bytes",
			info.Size(), limit, changedBytes)
	}
}

func assertRefSet(t *testing.T, refs []string, want ...string) {
	t.Helper()
	joined := strings.Join(refs, "\n")
	for _, ref := range want {
		if !strings.Contains(joined, ref) {
			t.Errorf("ref %s missing from %v", ref, refs)
		}
	}
	for _, gone := range []string{"refs/heads/feature", "refs/tags/v1"} {
		if strings.Contains(joined, gone) {
			t.Errorf("deleted ref %s came back: %v", gone, refs)
		}
	}
}

func gitRefs(t *testing.T, dir string) []string {
	t.Helper()
	out := runGit(t, dir, "for-each-ref", "--format=%(refname)")
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			refs = append(refs, line)
		}
	}
	sort.Strings(refs)
	return refs
}

// treeDigest folds every path, node type, symlink target, device
// number, and file content under root into one digest.
func treeDigest(t *testing.T, root string) string {
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
		line, lineErr := digestEntry(path, rel, entry)
		if lineErr != nil {
			return lineErr
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

func digestEntry(path, rel string, entry fs.DirEntry) (string, error) {
	switch {
	case entry.IsDir():
		return rel + " dir", nil
	case entry.Type()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		return rel + " link " + target, nil
	case entry.Type()&os.ModeCharDevice != 0:
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return "", errors.New("cannot read the device number")
		}
		return fmt.Sprintf("%s char %d", rel, st.Rdev), nil
	case entry.Type().IsRegular():
		sum, err := fileDigest(path)
		if err != nil {
			return "", err
		}
		return rel + " file " + sum, nil
	default:
		return rel + " other", nil
	}
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func dirBytes(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}
