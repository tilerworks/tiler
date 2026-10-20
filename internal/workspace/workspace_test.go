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
		RuntimeDir: filepath.Join(dir, "runtime", "m1"),
		MergedDir:  filepath.Join(dir, "merged"),
	}
	return req, dir
}

// mountTest runs mount with an injected runner and the fake probe
// seams, skipping where this host cannot mount at all.
func mountTest(t *testing.T, req Request, run runFunc, seam hooks) (*Workspace, error) {
	t.Helper()
	w, err := mount(req, run, seam)
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("mounting unavailable here: %v", err)
	}
	return w, err
}

// stackLowerEntries builds the squashfuse_ll transit rows the mount
// table would carry for a live stack of req's members under one
// runtime directory, in mount order (newest increment first). It
// mirrors the identity the engine publishes so a reuse test can
// pin the table to the exact request. Members come from stack(req),
// which fails only when the stack overflows the depth limit; that
// never happens for a test fixture.
func stackLowerEntries(t *testing.T, runtime string, req Request) []Entry {
	t.Helper()
	members, err := stack(req)
	if err != nil {
		t.Fatalf("stack: %v", err)
	}
	lowers := plannedLowers(runtime, len(members))
	entries := make([]Entry, 0, len(members))
	for i, m := range members {
		entries = append(entries, Entry{
			Point:  lowers[i],
			FSType: "fuse",
			Source: m.fsname,
		})
	}
	return entries
}

func TestMountOrdersLowersNewestFirst(t *testing.T) {
	req, dir := testRequest(t)
	rec := &recRunner{}
	w, err := mountTest(t, req, rec.run, testHooks())
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
	if !strings.HasPrefix(w.UpperDir, req.RuntimeDir+string(filepath.Separator)) {
		t.Errorf("upper dir %s is not under the combination directory %s",
			w.UpperDir, req.RuntimeDir)
	}
	if info, err := os.Stat(req.MergedDir); err != nil || !info.IsDir() {
		t.Errorf("merged dir was not created: %v", err)
	}
}

func TestUnmountRunsInReverseOrder(t *testing.T) {
	req, _ := testRequest(t)
	rec := &recRunner{}
	w, err := mountTest(t, req, rec.run, testHooks())
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
	_, err := mountTest(t, req, rec.run, testHooks())
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
	_, err := mountTest(t, req, rec.run, testHooks())
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
	_, err := mountTest(t, req, rec.run, testHooks())
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
	w, err := mountTest(t, req, rec.run, testHooks())
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
	w, err := mountTest(t, req, rec.run, testHooks())
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
	if _, err := mountTest(t, req, rec.run, testHooks()); err != nil {
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
	if _, err := mountTest(t, req, rec.run, testHooks()); err != nil {
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
	if _, err := mountTest(t, req, rec.run, seam); err != nil {
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
	_, err := mountTest(t, req, rec.run, seam)
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
	_, err := mountTest(t, req, rec.run, testHooks())
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

// subdirRunner is a recording runner that reports the subdir as
// missing for chosen member byte paths.
type subdirRunner struct {
	recRunner
	missing map[string]bool
}

func (r *subdirRunner) run(name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	if name == "unsquashfs" && len(args) > 2 && r.missing[args[2]] {
		return errors.New("unsquashfs: Pathname not found")
	}
	return nil
}

func TestMountRejectsBadSubdirShape(t *testing.T) {
	tests := []struct {
		subdir  string
		wantErr bool
	}{
		{subdir: "src/lib"},
		{subdir: "a"},
		{subdir: "/abs/path", wantErr: true},
		{subdir: "../up", wantErr: true},
		{subdir: "a/../b", wantErr: true},
		{subdir: "a//b", wantErr: true},
		{subdir: "./a", wantErr: true},
		{subdir: "a/", wantErr: true},
		{subdir: ".", wantErr: true},
		{subdir: "a/./b", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.subdir, func(t *testing.T) {
			req, _ := testRequest(t)
			req.Layers = nil
			req.Subdir = tc.subdir
			rec := &recRunner{}
			_, err := mountTest(t, req, rec.run, testHooks())
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("subdir %q refused: %v", tc.subdir, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "subdir") {
				t.Fatalf("err = %v, want a subdir shape refusal", err)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("tools ran despite the bad shape: %v", rec.calls)
			}
		})
	}
}

func TestMountSubdirSkipsMembersWithoutIt(t *testing.T) {
	req, _ := testRequest(t)
	req.Subdir = "src"
	rec := &subdirRunner{missing: map[string]bool{req.Layers[0]: true}}
	w, err := mountTest(t, req, rec.run, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	probes := rec.find("unsquashfs")
	if len(probes) != 3 {
		t.Fatalf("unsquashfs calls = %v", probes)
	}
	for i, call := range probes {
		if call[1] != "-ls" || call[2] != "-match" || call[4] != "src" {
			t.Errorf("probe %d = %q", i, call)
		}
	}
	squash := rec.find("squashfuse_ll")
	if len(squash) != 2 {
		t.Fatalf("squashfuse_ll calls = %v", squash)
	}
	// The layer without the subtree is dropped; the newest surviving
	// layer takes lower-0 and the full image stays last.
	wantBytes := []string{req.Layers[1], req.Full}
	for i, call := range squash {
		if call[3] != wantBytes[i] {
			t.Errorf("member %d bytes = %q, want %q", i, call[3], wantBytes[i])
		}
		if call[2] != "fsname="+wantBytes[i]+",subdir=src" {
			t.Errorf("member %d opts = %q", i, call[2])
		}
	}
	if len(w.mounts) != 3 {
		t.Errorf("mounts = %v", w.mounts)
	}
}

func TestMountSubdirAllMissingFailsCleanly(t *testing.T) {
	req, _ := testRequest(t)
	req.Subdir = "src"
	rec := &subdirRunner{missing: map[string]bool{
		req.Full: true, req.Layers[0]: true, req.Layers[1]: true,
	}}
	_, err := mountTest(t, req, rec.run, testHooks())
	if err == nil {
		t.Fatal("want an all-missing refusal")
	}
	for _, want := range []string{"subdir src", req.Full, req.Layers[0], req.Layers[1]} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
	for _, name := range []string{"squashfuse_ll", "fuse-overlayfs", "fusermount3"} {
		if calls := rec.find(name); len(calls) != 0 {
			t.Errorf("%s ran despite the refusal: %v", name, calls)
		}
	}
	for _, dir := range []string{req.MergedDir, req.RuntimeDir} {
		if _, statErr := os.Stat(dir); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("refusal left %s behind: %v", dir, statErr)
		}
	}
}

func TestMountSubdirSingleSurvivorMountsDirect(t *testing.T) {
	req, _ := testRequest(t)
	req.ReadOnly = true
	req.Subdir = "src"
	rec := &subdirRunner{missing: map[string]bool{
		req.Layers[0]: true, req.Layers[1]: true,
	}}
	w, err := mountTest(t, req, rec.run, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	squash := rec.find("squashfuse_ll")
	if len(squash) != 1 {
		t.Fatalf("squashfuse_ll calls = %v", squash)
	}
	if squash[0][2] != "fsname="+req.Full+",subdir=src" ||
		squash[0][3] != req.Full || squash[0][4] != req.MergedDir {
		t.Errorf("direct mount = %q", squash[0])
	}
	if len(rec.find("fuse-overlayfs")) != 0 {
		t.Errorf("a lone survivor ran fuse-overlayfs: %v", rec.calls)
	}
	if _, statErr := os.Stat(req.RuntimeDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a direct mount created the runtime dir: %v", statErr)
	}
	if len(w.mounts) != 1 || w.mounts[0] != req.MergedDir {
		t.Errorf("mounts = %v", w.mounts)
	}
}

// fakeKernel tracks mount state across injected runs: a successful
// mount tool marks the target point live and fusermount3 clears it.
// repair, when set, mutates the file system on an unmount so a test
// can expose the plain directory under a cleaned stale mount.
type fakeKernel struct {
	recRunner
	live     map[string]bool
	lazyOnly map[string]bool
	repair   func(point string)
}

func newFakeKernel() *fakeKernel {
	return &fakeKernel{
		live:     map[string]bool{},
		lazyOnly: map[string]bool{},
	}
}

func (k *fakeKernel) run(name string, args ...string) error {
	k.calls = append(k.calls, append([]string{name}, args...))
	switch name {
	case "squashfuse_ll", "fuse-overlayfs", "concatfs":
		k.live[args[len(args)-1]] = true
	case "fusermount3":
		point := args[len(args)-1]
		if args[0] == "-u" && k.lazyOnly[point] {
			return errors.New("fusermount3: failed to unmount (device or resource busy)")
		}
		k.live[point] = false
		if k.repair != nil {
			k.repair(point)
		}
	}
	return nil
}

func (k *fakeKernel) isLive(point string) bool { return k.live[point] }

func (k *fakeKernel) seam() hooks {
	return hooks{live: k.isLive, sleep: func(time.Duration) {}}
}

func TestUnderPrefix(t *testing.T) {
	tests := []struct {
		dir    string
		prefix string
		want   bool
	}{
		{dir: "/rt/up-1-2/upper", prefix: "/rt", want: true},
		{dir: "/rt", prefix: "/rt", want: true},
		{dir: "/rt2/x", prefix: "/rt", want: false},
		{dir: "/rt/x/../y", prefix: "/rt", want: true},
		{dir: "/mnt/view", prefix: "/rt", want: false},
	}
	for _, tc := range tests {
		if got := underPrefix(tc.dir, tc.prefix); got != tc.want {
			t.Errorf("underPrefix(%q, %q) = %v, want %v", tc.dir, tc.prefix, got, tc.want)
		}
	}
}

func TestCheckOwner(t *testing.T) {
	if err := checkOwner(1000, 1000); err != nil {
		t.Fatalf("matching owner refused: %v", err)
	}
	err := checkOwner(0, 1000)
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("err = %v, want an owner refusal", err)
	}
}

func TestMountRejectsTargetUnderRuntime(t *testing.T) {
	req, _ := testRequest(t)
	req.MergedDir = filepath.Join(req.RuntimeDir, "view")
	rec := &recRunner{}
	_, err := mountTest(t, req, rec.run, testHooks())
	if err == nil || !strings.Contains(err.Error(), "inside the runtime tree") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("tools ran despite the refusal: %v", rec.calls)
	}
}

func TestMountRejectsRuntimeUnderTarget(t *testing.T) {
	req, _ := testRequest(t)
	req.RuntimeDir = filepath.Join(req.MergedDir, "runtime", "m1")
	rec := &recRunner{}
	_, err := mountTest(t, req, rec.run, testHooks())
	if err == nil || !strings.Contains(err.Error(), "inside the mount target") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("tools ran despite the refusal: %v", rec.calls)
	}
}

func TestMountRejectsNonEmptyTarget(t *testing.T) {
	req, _ := testRequest(t)
	if err := os.MkdirAll(req.MergedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(req.MergedDir, "precious.txt")
	if err := os.WriteFile(precious, []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &recRunner{}
	_, err := mountTest(t, req, rec.run, testHooks())
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("tools ran despite the refusal: %v", rec.calls)
	}
	assertBytesFile(t, precious, []byte("user data"))
}

func TestMountRejectsFileTarget(t *testing.T) {
	req, _ := testRequest(t)
	if err := os.WriteFile(req.MergedDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &recRunner{}
	_, err := mountTest(t, req, rec.run, testHooks())
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("tools ran despite the refusal: %v", rec.calls)
	}
}

// assertBytesFile keeps the user-data assertions readable.
func assertBytesFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func TestMountReusesLiveReadWriteView(t *testing.T) {
	req, _ := testRequest(t)
	if err := os.MkdirAll(req.MergedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := req.RuntimeDir
	req.Table = append([]Entry{{
		Point:  req.MergedDir,
		FSType: "fuse.fuse-overlayfs",
		Source: "overlay",
		Upper:  filepath.Join(runtime, "upper"),
		Lowers: plannedLowers(runtime, 3),
	}}, stackLowerEntries(t, runtime, req)...)
	rec := &recRunner{}
	w, err := mountTest(t, req, rec.run, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if !w.Reused {
		t.Error("a matching live view must be reused")
	}
	if w.UpperDir != filepath.Join(runtime, "upper") {
		t.Errorf("reused upper = %s", w.UpperDir)
	}
	if len(rec.calls) != 0 {
		t.Errorf("reuse ran commands: %v", rec.calls)
	}
	if len(w.mounts) != 0 {
		t.Errorf("reuse recorded mounts: %v", w.mounts)
	}
}

func TestMountReusesLiveReadOnlyDirect(t *testing.T) {
	req, _ := testRequest(t)
	req.ReadOnly = true
	req.Layers = nil
	if err := os.MkdirAll(req.MergedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	req.Table = []Entry{{
		Point: req.MergedDir, FSType: "fuse", Source: req.Full,
	}}
	rec := &recRunner{}
	w, err := mountTest(t, req, rec.run, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if !w.Reused || w.UpperDir != "" || len(rec.calls) != 0 {
		t.Fatalf("reused = %v upper = %q calls = %v", w.Reused, w.UpperDir, rec.calls)
	}
}

func TestMountReusesLiveReadOnlyChain(t *testing.T) {
	req, _ := testRequest(t)
	req.ReadOnly = true
	if err := os.MkdirAll(req.MergedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := req.RuntimeDir
	req.Table = append([]Entry{{
		Point:  req.MergedDir,
		FSType: "fuse.fuse-overlayfs",
		Source: "overlay",
		Lowers: plannedLowers(runtime, 3),
	}}, stackLowerEntries(t, runtime, req)...)
	rec := &recRunner{}
	w, err := mountTest(t, req, rec.run, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if !w.Reused || w.UpperDir != "" || len(rec.calls) != 0 {
		t.Fatalf("reused = %v upper = %q calls = %v", w.Reused, w.UpperDir, rec.calls)
	}
}

func TestMountConflictsOnDifferentIdentity(t *testing.T) {
	tests := []struct {
		name     string
		readOnly bool
		layers   bool
		entry    func(req Request) Entry
		// lowers optionally appends squashfuse_ll transit rows to
		// the injected table. Nil leaves only the merged entry.
		lowers func(t *testing.T, req Request) []Entry
	}{
		{
			name: "read-write with a foreign upper",
			entry: func(req Request) Entry {
				return Entry{
					Point: req.MergedDir, Source: "overlay",
					Upper:  "/elsewhere/up-1-2/upper",
					Lowers: plannedLowers("/elsewhere/up-1-2", 3),
				}
			},
		},
		{
			name: "read-write with the wrong layer count",
			entry: func(req Request) Entry {
				runtime := req.RuntimeDir
				return Entry{
					Point: req.MergedDir, Source: "overlay",
					Upper:  filepath.Join(runtime, "upper"),
					Lowers: plannedLowers(runtime, 2),
				}
			},
		},
		{
			name:     "read-only direct with a different image",
			readOnly: true,
			entry: func(req Request) Entry {
				return Entry{Point: req.MergedDir, FSType: "fuse", Source: "/other/image.sqsh"}
			},
		},
		{
			name:     "read-only chain carrying an upper",
			readOnly: true,
			layers:   true,
			entry: func(req Request) Entry {
				runtime := req.RuntimeDir
				return Entry{
					Point: req.MergedDir, Source: "overlay",
					Upper:  filepath.Join(runtime, "upper"),
					Lowers: plannedLowers(runtime, 3),
				}
			},
		},
		{
			// The overlay shape matches exactly, but the newest
			// increment's transit point still serves a foreign
			// image. Without a source-level check this would
			// silently reuse the wrong view (P4).
			name:   "read-write over a live lower serving a different image",
			layers: true,
			entry: func(req Request) Entry {
				runtime := req.RuntimeDir
				return Entry{
					Point: req.MergedDir, Source: "overlay",
					Upper:  filepath.Join(runtime, "upper"),
					Lowers: plannedLowers(runtime, 3),
				}
			},
			lowers: func(t *testing.T, req Request) []Entry {
				runtime := req.RuntimeDir
				entries := stackLowerEntries(t, runtime, req)
				entries[0].Source = "/elsewhere/foreign.sqsh"
				return entries
			},
		},
		{
			// The overlay shape matches, but the mount table lost
			// the base image's transit row. The identity check
			// cannot confirm the live view serves the requested
			// stack, so it conservatively refuses to reuse.
			name:   "read-write with a missing lower transit row",
			layers: true,
			entry: func(req Request) Entry {
				runtime := req.RuntimeDir
				return Entry{
					Point: req.MergedDir, Source: "overlay",
					Upper:  filepath.Join(runtime, "upper"),
					Lowers: plannedLowers(runtime, 3),
				}
			},
			lowers: func(t *testing.T, req Request) []Entry {
				runtime := req.RuntimeDir
				entries := stackLowerEntries(t, runtime, req)
				return entries[:len(entries)-1]
			},
		},
		{
			// Same three-member shape, same runtime directory,
			// same lower paths, but every transit point serves a
			// different image. This is the concrete P4 replay:
			// mount alice/app, then mount bob/tools on the same
			// target while alice/app's view is live.
			name:   "read-write with every lower serving a different image",
			layers: true,
			entry: func(req Request) Entry {
				runtime := req.RuntimeDir
				return Entry{
					Point: req.MergedDir, Source: "overlay",
					Upper:  filepath.Join(runtime, "upper"),
					Lowers: plannedLowers(runtime, 3),
				}
			},
			lowers: func(_ *testing.T, req Request) []Entry {
				runtime := req.RuntimeDir
				lowers := plannedLowers(runtime, 3)
				return []Entry{
					{Point: lowers[0], FSType: "fuse", Source: "/other/inc2.sqsh"},
					{Point: lowers[1], FSType: "fuse", Source: "/other/inc1.sqsh"},
					{Point: lowers[2], FSType: "fuse", Source: "/other/image.sqsh"},
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := testRequest(t)
			req.ReadOnly = tc.readOnly
			if !tc.layers {
				req.Layers = nil
			}
			if err := os.MkdirAll(req.MergedDir, 0o755); err != nil {
				t.Fatal(err)
			}
			req.Table = []Entry{tc.entry(req)}
			if tc.lowers != nil {
				req.Table = append(req.Table, tc.lowers(t, req)...)
			}
			rec := &recRunner{}
			_, err := mountTest(t, req, rec.run, testHooks())
			if err == nil || !strings.Contains(err.Error(), "different view") {
				t.Fatalf("err = %v, want a conflict", err)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("a conflict ran commands: %v", rec.calls)
			}
		})
	}
}

func TestMountSubdirCannotReuseLiveEntry(t *testing.T) {
	req, _ := testRequest(t)
	req.ReadOnly = true
	req.Layers = nil
	req.Subdir = "src"
	if err := os.MkdirAll(req.MergedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	req.Table = []Entry{{Point: req.MergedDir, FSType: "fuse", Source: req.Full}}
	rec := &recRunner{}
	_, err := mountTest(t, req, rec.run, testHooks())
	if err == nil || !strings.Contains(err.Error(), "cannot show a subdir") {
		t.Fatalf("err = %v, want an unverifiable-subdir conflict", err)
	}
	if len(rec.find("squashfuse_ll")) != 0 || len(rec.find("fuse-overlayfs")) != 0 {
		t.Fatalf("a conflicted reuse ran mount commands: %v", rec.calls)
	}
}

func TestMountCleansStaleTargetThenMounts(t *testing.T) {
	req, dir := testRequest(t)
	req.ReadOnly = true
	req.Layers = nil
	// A regular file where the target's parent belongs makes every
	// stat of the target fail, which is how a dead mount reads
	// before the cleanup exposes the plain directory underneath.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	req.MergedDir = filepath.Join(blocker, "merged")
	req.Table = []Entry{{Point: req.MergedDir, FSType: "fuse", Source: req.Full}}
	k := newFakeKernel()
	k.lazyOnly[req.MergedDir] = true
	k.repair = func(point string) {
		if point != req.MergedDir {
			return
		}
		if err := os.Remove(blocker); err != nil {
			t.Error(err)
			return
		}
		if err := os.MkdirAll(point, 0o755); err != nil {
			t.Error(err)
		}
	}
	w, err := mountTest(t, req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if w.Reused {
		t.Error("a stale entry must never reuse")
	}
	unmounts := k.find("fusermount3")
	if len(unmounts) != 2 || unmounts[0][1] != "-u" || unmounts[1][1] != "-uz" ||
		unmounts[0][2] != req.MergedDir || unmounts[1][2] != req.MergedDir {
		t.Fatalf("stale cleanup calls = %v, want -u then -uz", unmounts)
	}
	squash := k.find("squashfuse_ll")
	if len(squash) != 1 || squash[0][4] != req.MergedDir {
		t.Fatalf("squashfuse_ll calls = %v", squash)
	}
	if len(w.mounts) != 1 || w.mounts[0] != req.MergedDir {
		t.Errorf("mounts = %v", w.mounts)
	}
}

func TestMountTearsDownListedEntryWithDeadDaemon(t *testing.T) {
	req, _ := testRequest(t)
	req.ReadOnly = true
	req.Layers = nil
	if err := os.MkdirAll(req.MergedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The kernel-side mount is still attached and stat keeps
	// answering from the attribute cache, but the daemon behind it
	// is gone: the entry reads anonymous and must never be reused.
	req.Table = []Entry{{
		Point:  req.MergedDir,
		FSType: "fuse.fuse-overlayfs",
		Source: "fuse-overlayfs",
	}}
	k := newFakeKernel()
	k.live[req.MergedDir] = true
	seam := hooks{
		live:    k.isLive,
		serving: func(string) bool { return false },
		sleep:   func(time.Duration) {},
	}
	w, err := mountTest(t, req, k.run, seam)
	if err != nil {
		t.Fatal(err)
	}
	if w.Reused {
		t.Error("an entry with a dead daemon must never be reused")
	}
	unmounts := k.find("fusermount3")
	if len(unmounts) != 1 || unmounts[0][1] != "-u" || unmounts[0][2] != req.MergedDir {
		t.Fatalf("teardown calls = %v, want a single -u of the target", unmounts)
	}
	squash := k.find("squashfuse_ll")
	if len(squash) != 1 || squash[0][4] != req.MergedDir {
		t.Fatalf("squashfuse_ll calls = %v, want one fresh direct mount", squash)
	}
}

// A listed-but-dead overlay that still names upper/lowers under the
// session-scoped combination directory must tear those squashfuse
// lowers down; otherwise a remount into a different m<seq> leaves
// orphan FUSE mounts behind.
func TestMountTearsDownDeadEntryCombinationLowers(t *testing.T) {
	req, _ := testRequest(t)
	if err := os.MkdirAll(req.MergedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := req.RuntimeDir
	upper := filepath.Join(runtime, "upper")
	lower0 := filepath.Join(runtime, "lower-0")
	lower1 := filepath.Join(runtime, "lower-1")
	for _, dir := range []string{upper, lower0, lower1} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	req.Table = []Entry{{
		Point:  req.MergedDir,
		FSType: "fuse.fuse-overlayfs",
		Source: "overlay",
		Upper:  upper,
		Lowers: []string{lower0, lower1},
	}}
	k := newFakeKernel()
	k.live[req.MergedDir] = true
	k.live[lower0] = true
	k.live[lower1] = true
	seam := hooks{
		live:    k.isLive,
		serving: func(string) bool { return false },
		sleep:   func(time.Duration) {},
	}
	w, err := mountTest(t, req, k.run, seam)
	if err != nil {
		t.Fatal(err)
	}
	if w.Reused {
		t.Error("a dead combination entry must never be reused")
	}
	unmounts := k.find("fusermount3")
	// Target first, then lowers newest-to-oldest under the
	// combination directory; later mount commands may add more.
	want := []string{req.MergedDir, lower1, lower0}
	if len(unmounts) < len(want) {
		t.Fatalf("teardown calls = %v, want at least -u of %v", unmounts, want)
	}
	for i, point := range want {
		if unmounts[i][1] != "-u" || unmounts[i][2] != point {
			t.Fatalf("teardown calls = %v, want -u of %v first", unmounts, want)
		}
	}
}

func TestMountCleansResidueOnPlannedLowerPoints(t *testing.T) {
	req, _ := testRequest(t)
	if err := os.MkdirAll(req.MergedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := req.RuntimeDir
	residue := filepath.Join(runtime, "lower-1")
	if err := os.MkdirAll(residue, 0o755); err != nil {
		t.Fatal(err)
	}
	listed := filepath.Join(runtime, "lower-2")
	if err := os.MkdirAll(listed, 0o755); err != nil {
		t.Fatal(err)
	}
	k := newFakeKernel()
	k.live[residue] = true
	// A table row whose point reads as a plain directory is already
	// gone; it must clean up silently without any command.
	req.Table = []Entry{{Point: listed, FSType: "fuse", Source: req.Full}}

	w, err := mountTest(t, req, k.run, k.seam())
	if err != nil {
		t.Fatal(err)
	}
	if w.Reused {
		t.Error("residue cleanup must not report reuse")
	}
	unmounts := k.find("fusermount3")
	if len(unmounts) != 1 || unmounts[0][2] != residue {
		t.Fatalf("cleanup calls = %v, want only the live residue", unmounts)
	}
	if k.calls[0][0] != "fusermount3" {
		t.Errorf("cleanup ran after mount commands: %v", k.calls[0])
	}
	if len(w.mounts) != 4 {
		t.Errorf("mounts = %v, want three lowers plus the merged view", w.mounts)
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
	all := []string{"squashfuse_ll", "fusermount3", "fuse-overlayfs", "unsquashfs", "concatfs"}
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
			name:    "a concat route needs concatfs",
			present: []string{"squashfuse_ll", "fusermount3"},
			req: Request{
				ReadOnly: true,
				Concat:   &ConcatSource{BlocksDir: "blocks", ViewName: "image.concat"},
			},
			wantErr: "concatfs is missing",
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
