package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

func TestMountOrdersLowersNewestFirst(t *testing.T) {
	req, dir := testRequest(t)
	rec := &recRunner{}
	req.Run = rec.run
	w, err := Mount(req)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			t.Skipf("mounting unavailable here: %v", err)
		}
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
	req.Run = rec.run
	w, err := Mount(req)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			t.Skipf("mounting unavailable here: %v", err)
		}
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
	req.Run = rec.run
	_, err := Mount(req)
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
}

func TestMountRequiresFields(t *testing.T) {
	req, _ := testRequest(t)
	rec := &recRunner{}
	req.Run = rec.run
	req.Full = ""
	_, err := Mount(req)
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("tools ran despite the missing field: %v", rec.calls)
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
