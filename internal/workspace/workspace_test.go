package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type recRunner struct {
	calls [][]string
	fail  string
}

func (r *recRunner) run(name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.fail != "" && name == r.fail {
		return errors.New(name + ": forced failure")
	}
	return nil
}

func (r *recRunner) find(name string) [][]string {
	var out [][]string
	for _, call := range r.calls {
		if call[0] == name {
			out = append(out, call)
		}
	}
	return out
}

// testHooks keeps unit tests off the real mount state: every point
// looks alive at once and the poll clock never waits.
func testHooks() hooks {
	return hooks{
		live:  func(string) bool { return true },
		sleep: func(time.Duration) {},
	}
}

func testRequest(t *testing.T) (Request, string) {
	t.Helper()
	dir := t.TempDir()
	req := Request{
		Full:       filepath.Join(dir, "image.sqsh"),
		Layers:     []string{filepath.Join(dir, "inc1.sqsh"), filepath.Join(dir, "inc2.sqsh")},
		RuntimeDir: filepath.Join(dir, "runtime"),
		MergedDir:  filepath.Join(dir, "merged"),
	}
	return req, dir
}

// mountTest runs mount with the recording runner and the fake probe
// seams, skipping where this host cannot mount at all.
func mountTest(t *testing.T, req Request, rec *recRunner, seam hooks) (*Workspace, error) {
	t.Helper()
	w, err := mount(req, rec.run, seam)
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("mounting unavailable here: %v", err)
	}
	return w, err
}

func TestMountOrdersLowersNewestFirst(t *testing.T) {
	req, dir := testRequest(t)
	rec := &recRunner{}
	w, err := mountTest(t, req, rec, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	squash := rec.find("squashfuse_ll")
	if len(squash) != 3 {
		t.Fatalf("squashfuse_ll calls = %v", squash)
	}
	// Newest increment first, full image last; fsname carries the
	// real library path so the mount table can identify the image.
	wantSources := []string{req.Layers[1], req.Layers[0], req.Full}
	for i, call := range squash {
		if call[2] != "fsname="+wantSources[i] || call[3] != wantSources[i] {
			t.Errorf("call %d = %q", i, call)
		}
		point := call[4]
		if !strings.HasPrefix(point, filepath.Join(dir, "runtime")) {
			t.Errorf("lower point %s is outside the runtime dir", point)
		}
		if w.mounts[i] != point {
			t.Errorf("mount order %v does not match call order", w.mounts)
		}
	}
	overlay := rec.find("fuse-overlayfs")
	if len(overlay) != 1 {
		t.Fatalf("fuse-overlayfs calls = %v", overlay)
	}
	opts := overlay[0][2]
	lowerdir := strings.Join([]string{squash[0][4], squash[1][4], squash[2][4]}, ":")
	for _, want := range []string{
		"lowerdir=" + lowerdir,
		"upperdir=" + w.UpperDir,
		"workdir=" + filepath.Join(filepath.Dir(w.UpperDir), "work"),
	} {
		if !strings.Contains(opts, want) {
			t.Errorf("overlay opts %q missing %q", opts, want)
		}
	}
	if overlay[0][3] != req.MergedDir {
		t.Errorf("merged point = %q", overlay[0][3])
	}
	if !strings.HasPrefix(w.UpperDir, filepath.Join(dir, "runtime", "up-")) {
		t.Errorf("upper dir %s is not under runtime/up-<dev>-<ino>", w.UpperDir)
	}
	if info, err := os.Stat(req.MergedDir); err != nil || !info.IsDir() {
		t.Errorf("merged dir was not created: %v", err)
	}
}

func TestUnmountRunsInReverseOrder(t *testing.T) {
	req, _ := testRequest(t)
	rec := &recRunner{}
	w, err := mountTest(t, req, rec, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	rec.calls = nil
	points := append([]string(nil), w.mounts...)
	if err := w.Unmount(); err != nil {
		t.Fatal(err)
	}
	unmounts := rec.find("fusermount3")
	if len(unmounts) != 4 {
		t.Fatalf("fusermount3 calls = %v", unmounts)
	}
	for i, call := range unmounts {
		want := points[len(points)-1-i]
		if call[1] != "-u" || call[2] != want {
			t.Errorf("unmount %d = %q, want %s", i, call, want)
		}
	}
	if unmounts[0][2] != req.MergedDir {
		t.Errorf("merged view was not unmounted first: %v", unmounts[0])
	}
	if err := w.Unmount(); err != nil {
		t.Fatalf("second unmount should be a no-op: %v", err)
	}
}

func TestMountFailureUnmountsLowerPoints(t *testing.T) {
	req, _ := testRequest(t)
	rec := &recRunner{fail: "fuse-overlayfs"}
	_, err := mountTest(t, req, rec, testHooks())
	if err == nil || !strings.Contains(err.Error(), "forced failure") {
		t.Fatalf("err = %v", err)
	}
	unmounts := rec.find("fusermount3")
	if len(unmounts) != 3 {
		t.Fatalf("cleanup unmounts = %v", unmounts)
	}
	for i, want := range []string{"lower-2", "lower-1", "lower-0"} {
		if filepath.Base(unmounts[i][2]) != want {
			t.Errorf("cleanup %d = %q, want %s", i, unmounts[i][2], want)
		}
	}
	// The failed call also removes every directory it created.
	for _, dir := range []string{req.MergedDir, req.RuntimeDir} {
		if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("created dir %s survived the failure: %v", dir, statErr)
		}
	}
}

func TestMountRequiresFields(t *testing.T) {
	req, _ := testRequest(t)
	rec := &recRunner{}
	req.Full = ""
	_, err := mountTest(t, req, rec, testHooks())
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("tools ran despite the missing field: %v", rec.calls)
	}
}

func TestMountRefusesDeepStack(t *testing.T) {
	req, _ := testRequest(t)
	req.Layers = make([]string, maxStackDepth)
	rec := &recRunner{}
	_, err := mountTest(t, req, rec, testHooks())
	if err == nil || !strings.Contains(err.Error(), "the limit is 8") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("tools ran despite the deep stack: %v", rec.calls)
	}
}

func TestMountReadOnlySingleIsDirect(t *testing.T) {
	req, _ := testRequest(t)
	req.Layers = nil
	req.ReadOnly = true
	rec := &recRunner{}
	w, err := mountTest(t, req, rec, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	squash := rec.find("squashfuse_ll")
	if len(squash) != 1 {
		t.Fatalf("squashfuse_ll calls = %v", squash)
	}
	if squash[0][3] != req.Full || squash[0][4] != req.MergedDir {
		t.Errorf("direct mount = %q, want bytes %s on %s",
			squash[0], req.Full, req.MergedDir)
	}
	if len(rec.find("fuse-overlayfs")) != 0 {
		t.Errorf("a direct mount ran fuse-overlayfs: %v", rec.calls)
	}
	if w.UpperDir != "" {
		t.Errorf("read-only mount recorded upper %s", w.UpperDir)
	}
	if len(w.mounts) != 1 || w.mounts[0] != req.MergedDir {
		t.Errorf("mounts = %v", w.mounts)
	}
	if _, err := os.Stat(req.RuntimeDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a direct mount created the runtime dir: %v", err)
	}
	if info, err := os.Stat(req.MergedDir); err != nil || !info.IsDir() {
		t.Errorf("merged dir was not created: %v", err)
	}
}

func TestMountReadOnlyChainMergesLowerdirOnly(t *testing.T) {
	req, _ := testRequest(t)
	req.ReadOnly = true
	rec := &recRunner{}
	w, err := mountTest(t, req, rec, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.find("squashfuse_ll")) != 3 {
		t.Fatalf("squashfuse_ll calls = %v", rec.calls)
	}
	overlay := rec.find("fuse-overlayfs")
	if len(overlay) != 1 {
		t.Fatalf("fuse-overlayfs calls = %v", overlay)
	}
	opts := overlay[0][2]
	if !strings.HasPrefix(opts, "lowerdir=") {
		t.Errorf("overlay opts = %q", opts)
	}
	if strings.Contains(opts, "upperdir") || strings.Contains(opts, "workdir") {
		t.Errorf("read-only chain carries an upper: %q", opts)
	}
	if w.UpperDir != "" {
		t.Errorf("read-only mount recorded upper %s", w.UpperDir)
	}
	entries, err := os.ReadDir(w.runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "lower-") {
			t.Errorf("read-only runtime holds %s", entry.Name())
		}
	}
}

func TestMountFSNameOverridesForCacheCopies(t *testing.T) {
	req, _ := testRequest(t)
	req.FullFSName = "/nas/images/alice/app/image.sqsh"
	req.LayerFSNames = []string{
		"/nas/images/alice/app/incr/inc1.sqsh",
		"/nas/images/alice/app/incr/inc2.sqsh",
	}
	rec := &recRunner{}
	if _, err := mountTest(t, req, rec, testHooks()); err != nil {
		t.Fatal(err)
	}
	squash := rec.find("squashfuse_ll")
	want := []struct{ fsname, path string }{
		{req.LayerFSNames[1], req.Layers[1]},
		{req.LayerFSNames[0], req.Layers[0]},
		{req.FullFSName, req.Full},
	}
	if len(squash) != len(want) {
		t.Fatalf("squashfuse_ll calls = %v", squash)
	}
	for i, call := range squash {
		if call[2] != "fsname="+want[i].fsname {
			t.Errorf("call %d identity = %q, want %q", i, call[2], want[i].fsname)
		}
		if call[3] != want[i].path {
			t.Errorf("call %d bytes = %q, want %q", i, call[3], want[i].path)
		}
	}
}

func TestMountAppendsConfiguredOptions(t *testing.T) {
	req, _ := testRequest(t)
	req.Layers = nil
	req.Subdir = "src/lib"
	req.SquashfsOptions = "allow_other"
	req.FuseOptions = "squashfs_uid=1000"
	rec := &recRunner{}
	if _, err := mountTest(t, req, rec, testHooks()); err != nil {
		t.Fatal(err)
	}
	squash := rec.find("squashfuse_ll")
	wantOpts := "fsname=" + req.Full + ",subdir=src/lib,allow_other"
	if len(squash) != 1 || squash[0][2] != wantOpts {
		t.Fatalf("squashfuse_ll calls = %v, want opts %q", squash, wantOpts)
	}
	overlay := rec.find("fuse-overlayfs")
	if len(overlay) != 1 {
		t.Fatalf("fuse-overlayfs calls = %v", overlay)
	}
	if !strings.HasSuffix(overlay[0][2], ",squashfs_uid=1000") {
		t.Errorf("overlay opts = %q", overlay[0][2])
	}
}

func TestMountWaitsForSlowDaemon(t *testing.T) {
	req, _ := testRequest(t)
	req.Layers = nil
	req.ReadOnly = true
	rec := &recRunner{}
	var slept []time.Duration
	probes := 0
	seam := hooks{
		live: func(string) bool {
			probes++
			return probes >= 3
		},
		sleep: func(d time.Duration) { slept = append(slept, d) },
	}
	if _, err := mountTest(t, req, rec, seam); err != nil {
		t.Fatal(err)
	}
	if len(slept) != 2 {
		t.Fatalf("sleeps = %v, want two pauses before the mount looked live", slept)
	}
	for _, d := range slept {
		if d != liveInterval {
			t.Errorf("pause = %v, want %v", d, liveInterval)
		}
	}
}

func TestMountVanishedPointTearsDown(t *testing.T) {
	req, _ := testRequest(t)
	rec := &recRunner{}
	seam := hooks{
		live: func(point string) bool {
			return !strings.HasSuffix(point, "lower-1")
		},
		sleep: func(time.Duration) {},
	}
	_, err := mountTest(t, req, rec, seam)
	if err == nil || !strings.Contains(err.Error(), "did not become a live mount") {
		t.Fatalf("err = %v", err)
	}
	unmounts := rec.find("fusermount3")
	if len(unmounts) != 2 {
		t.Fatalf("cleanup unmounts = %v", unmounts)
	}
	for i, want := range []string{"lower-1", "lower-0"} {
		if filepath.Base(unmounts[i][2]) != want {
			t.Errorf("cleanup %d = %q, want %s", i, unmounts[i][2], want)
		}
	}
	for _, dir := range []string{req.MergedDir, req.RuntimeDir} {
		if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("created dir %s survived the failure: %v", dir, statErr)
		}
	}
}

func TestMountFailureRemovesCreatedAncestors(t *testing.T) {
	req, dir := testRequest(t)
	req.MergedDir = filepath.Join(dir, "deep", "nested", "merged")
	rec := &recRunner{fail: "fuse-overlayfs"}
	_, err := mountTest(t, req, rec, testHooks())
	if err == nil || !strings.Contains(err.Error(), "forced failure") {
		t.Fatalf("err = %v", err)
	}
	deep := filepath.Join(dir, "deep")
	if _, statErr := os.Stat(deep); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("ancestors created for the target survived: %v", statErr)
	}
	if _, statErr := os.Stat(req.RuntimeDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("runtime dir survived the failure: %v", statErr)
	}
}

func TestMountFailureReportsCleanupResidue(t *testing.T) {
	req, _ := testRequest(t)
	run := func(name string, _ ...string) error {
		if name == "fuse-overlayfs" || name == "fusermount3" {
			return errors.New(name + ": forced failure")
		}
		return nil
	}
	_, err := mount(req, run, testHooks())
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("mounting unavailable here: %v", err)
	}
	if err == nil {
		t.Fatal("want failure")
	}
	if !strings.Contains(err.Error(), "fuse-overlayfs: forced failure") {
		t.Errorf("primary cause lost: %v", err)
	}
	if !strings.Contains(err.Error(), "(cleanup:") {
		t.Errorf("cleanup residue missing: %v", err)
	}
}

func TestSquashfsArgvGolden(t *testing.T) {
	const (
		orig   = "/images/alice/app/image.sqsh"
		cached = "/cache/4f2a9c.sqsh"
		point  = "/runtime/up-1-2/lower-0"
	)
	tests := []struct {
		name    string
		fsname  string
		subdir  string
		options string
		path    string
		want    string
	}{
		{
			name:   "plain member publishes its own path",
			fsname: orig, path: orig,
			want: "fsname=" + orig,
		},
		{
			name:   "cache copy keeps the original identity",
			fsname: orig, path: cached,
			want: "fsname=" + orig,
		},
		{
			name:   "subdir joins the built-in option",
			fsname: orig, subdir: "src/lib", path: orig,
			want: "fsname=" + orig + ",subdir=src/lib",
		},
		{
			name:   "configured options come last",
			fsname: orig, options: "allow_other", path: cached,
			want: "fsname=" + orig + ",allow_other",
		},
		{
			name:   "subdir stays before configured options",
			fsname: orig, subdir: "src", options: "allow_other", path: orig,
			want: "fsname=" + orig + ",subdir=src,allow_other",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := squashfsArgv(tc.fsname, tc.subdir, tc.options, tc.path, point)
			want := []string{"squashfuse_ll", "-o", tc.want, tc.path, point}
			if !slices.Equal(got, want) {
				t.Fatalf("argv = %q, want %q", got, want)
			}
		})
	}
}

func TestOverlayArgvGolden(t *testing.T) {
	const merged = "/mnt/view"
	lowers := []string{"/runtime/up-1-2/lower-0", "/runtime/up-1-2/lower-1"}
	tests := []struct {
		name    string
		upper   string
		work    string
		options string
		want    string
	}{
		{
			name:  "read-write merge carries upper and work",
			upper: "/runtime/up-1-2/upper", work: "/runtime/up-1-2/work",
			want: "lowerdir=" + strings.Join(lowers, ":") +
				",upperdir=/runtime/up-1-2/upper,workdir=/runtime/up-1-2/work",
		},
		{
			name: "read-only chain merges lowerdir only",
			want: "lowerdir=" + strings.Join(lowers, ":"),
		},
		{
			name:    "configured options come last",
			upper:   "/runtime/up-1-2/upper",
			work:    "/runtime/up-1-2/work",
			options: "allow_other",
			want: "lowerdir=" + strings.Join(lowers, ":") +
				",upperdir=/runtime/up-1-2/upper,workdir=/runtime/up-1-2/work" +
				",allow_other",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := overlayArgv(lowers, tc.upper, tc.work, tc.options, merged)
			want := []string{"fuse-overlayfs", "-o", tc.want, merged}
			if !slices.Equal(got, want) {
				t.Fatalf("argv = %q, want %q", got, want)
			}
		})
	}
}

func TestPreflightToolchain(t *testing.T) {
	if err := available(); err != nil {
		t.Skipf("mounting unavailable here: %v", err)
	}
	all := []string{"squashfuse_ll", "fusermount3", "fuse-overlayfs", "unsquashfs"}
	tests := []struct {
		name    string
		present []string
		req     Request
		wantErr string
	}{
		{
			name:    "direct read-only needs no overlay tools",
			present: []string{"squashfuse_ll", "fusermount3"},
			req:     Request{ReadOnly: true},
		},
		{
			name:    "a read-only chain needs fuse-overlayfs",
			present: []string{"squashfuse_ll", "fusermount3"},
			req:     Request{ReadOnly: true, Layers: []string{"inc.sqsh"}},
			wantErr: "fuse-overlayfs is missing",
		},
		{
			name:    "a subdir needs unsquashfs",
			present: []string{"squashfuse_ll", "fusermount3", "fuse-overlayfs"},
			req:     Request{Subdir: "src"},
			wantErr: "unsquashfs is missing",
		},
		{
			name:    "the complete toolchain passes",
			present: all,
			req:     Request{Layers: []string{"inc.sqsh"}, Subdir: "src"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			for _, tool := range all {
				if !slices.Contains(tc.present, tool) {
					continue
				}
				script := filepath.Join(bin, tool)
				if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			err := preflight(tc.req)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("preflight = %v", err)
				}
				return
			}
			if err == nil || !errors.Is(err, ErrUnavailable) ||
				!strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("preflight = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestSmokeReadOnlyMountsAndUnmounts(t *testing.T) {
	if _, err := exec.LookPath("squashfuse_ll"); err != nil {
		t.Skipf("squashfuse_ll missing: %v", err)
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		t.Skipf("fusermount3 missing: %v", err)
	}
	rec := &recRunner{}
	image := filepath.Join(t.TempDir(), "layer.sqsh")
	if err := os.WriteFile(image, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := smokeReadOnly(image, rec.run)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			t.Skipf("mounting unavailable here: %v", err)
		}
		t.Fatal(err)
	}
	squash := rec.find("squashfuse_ll")
	if len(squash) != 1 || squash[0][1] != image {
		t.Fatalf("squashfuse_ll calls = %v", squash)
	}
	unmounts := rec.find("fusermount3")
	if len(unmounts) != 1 || unmounts[0][1] != "-u" || unmounts[0][2] != squash[0][2] {
		t.Fatalf("fusermount3 calls = %v", unmounts)
	}
	if _, statErr := os.Stat(squash[0][2]); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("smoke mount point left behind: %v", statErr)
	}
}

func TestSmokeReadOnlyReportsMountFailure(t *testing.T) {
	if _, err := exec.LookPath("squashfuse_ll"); err != nil {
		t.Skipf("squashfuse_ll missing: %v", err)
	}
	if _, err := exec.LookPath("fusermount3"); err != nil {
		t.Skipf("fusermount3 missing: %v", err)
	}
	rec := &recRunner{fail: "squashfuse_ll"}
	err := smokeReadOnly(filepath.Join(t.TempDir(), "layer.sqsh"), rec.run)
	if err == nil || !strings.Contains(err.Error(), "smoke mount") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.find("fusermount3")) != 0 {
		t.Error("unmount ran although nothing was mounted")
	}
}
