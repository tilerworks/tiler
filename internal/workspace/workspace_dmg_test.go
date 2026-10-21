package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/container"
)

type fakeHdiutil struct {
	calls      [][]string
	failDetach bool
	info       []byte
	infoErr    error
}

func (f *fakeHdiutil) run(argv []string, consErr error) ([]byte, error) {
	if consErr != nil {
		return nil, consErr
	}
	f.calls = append(f.calls, append([]string(nil), argv...))
	if len(argv) > 1 && argv[1] == "detach" && f.failDetach {
		return []byte("Resource busy"), errors.New("hdiutil: Resource busy")
	}
	if len(argv) > 1 && argv[1] == "info" {
		return f.info, f.infoErr
	}
	return nil, nil
}

func dmgFixture(t *testing.T, readOnly bool) (Request, string) {
	t.Helper()
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	shadow, err := container.DMGShadowPath(sessions, "dev", 1)
	if err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(root, "images", "alice", "app", "image.dmg")
	if err := os.MkdirAll(filepath.Dir(image), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("dmg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	merged := filepath.Join(root, "view")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	return Request{
		Full:       image,
		Format:     "dmg",
		ReadOnly:   readOnly,
		RuntimeDir: filepath.Dir(shadow),
		MergedDir:  merged,
	}, shadow
}

func TestDMGFirstMountPassesShadowWithoutCopy(t *testing.T) {
	req, shadow := dmgFixture(t, false)
	fake := &fakeHdiutil{}
	req.Hdiutil = fake.run
	w, err := mount(req, nil, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if w.Reused || w.UpperDir != shadow || len(w.mounts) != 1 {
		t.Fatalf("reused=%v upper=%q mounts=%v", w.Reused, w.UpperDir, w.mounts)
	}
	want, err := container.HdiutilAttachWritableArgv(req.Full, req.MergedDir, shadow)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 || !slices.Equal(fake.calls[0], want) {
		t.Fatalf("calls = %q, want %q", fake.calls, want)
	}
	if slices.Contains(fake.calls[0], "-force") {
		t.Fatalf("attach used -force: %q", fake.calls[0])
	}
	if _, statErr := os.Stat(shadow); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("shadow file = %v, want missing", statErr)
	}
	for _, name := range []string{"upper", "work"} {
		path := filepath.Join(req.RuntimeDir, name)
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s was created: %v", name, statErr)
		}
	}
	body, err := os.ReadFile(req.Full)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "dmg-bytes" {
		t.Fatalf("image bytes = %q", body)
	}
}

func TestDMGRemountReusesShadow(t *testing.T) {
	req, shadow := dmgFixture(t, false)
	req.Table = []Entry{{
		Point: req.MergedDir, FSType: "dmg", Source: req.Full, Upper: shadow,
	}}
	fake := &fakeHdiutil{}
	req.Hdiutil = fake.run
	w, err := mount(req, nil, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if !w.Reused || w.UpperDir != shadow || len(w.mounts) != 0 {
		t.Fatalf("reused=%v upper=%q mounts=%v", w.Reused, w.UpperDir, w.mounts)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("attach ran on reuse: %q", fake.calls)
	}
	if _, statErr := os.Stat(shadow); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("reuse created a shadow: %v", statErr)
	}
}

func TestDMGIdentityConflictDoesNotAttach(t *testing.T) {
	req, shadow := dmgFixture(t, false)
	other, err := container.DMGShadowPath(filepath.Dir(filepath.Dir(filepath.Dir(req.RuntimeDir))), "other", 2)
	if err != nil {
		t.Fatal(err)
	}
	req.Table = []Entry{{
		Point: req.MergedDir, FSType: "dmg", Source: req.Full, Upper: other,
	}}
	fake := &fakeHdiutil{}
	req.Hdiutil = fake.run
	_, err = mount(req, nil, testHooks())
	if err == nil || !strings.Contains(err.Error(), "different view") {
		t.Fatalf("err = %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("attach ran on conflict: %q", fake.calls)
	}
	if _, statErr := os.Stat(shadow); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("conflict created a shadow: %v", statErr)
	}
	if _, statErr := os.Stat(other); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("conflict copied another shadow: %v", statErr)
	}
}

func TestDMGReadOnlyAttachOmitsShadow(t *testing.T) {
	req, shadow := dmgFixture(t, true)
	fake := &fakeHdiutil{}
	req.Hdiutil = fake.run
	w, err := mount(req, nil, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if w.Reused || w.UpperDir != "" {
		t.Fatalf("reused=%v upper=%q", w.Reused, w.UpperDir)
	}
	want, err := container.HdiutilAttachReadOnlyArgv(req.Full, req.MergedDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 || !slices.Equal(fake.calls[0], want) {
		t.Fatalf("calls = %q, want %q", fake.calls, want)
	}
	if slices.Contains(fake.calls[0], "-shadow") {
		t.Fatalf("read-only attach used -shadow: %q", fake.calls[0])
	}
	if _, statErr := os.Stat(shadow); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("read-only created a shadow: %v", statErr)
	}
	if _, statErr := os.Stat(req.RuntimeDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("read-only created the runtime dir: %v", statErr)
	}

	req.Table = []Entry{{
		Point: req.MergedDir, FSType: "dmg", Source: req.Full,
	}}
	again, err := mount(req, nil, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if !again.Reused || len(fake.calls) != 1 {
		t.Fatalf("reused=%v calls=%q", again.Reused, fake.calls)
	}
}

func TestDMGDetachFailureKeepsMount(t *testing.T) {
	req, shadow := dmgFixture(t, false)
	fake := &fakeHdiutil{failDetach: true}
	req.Hdiutil = fake.run
	w, err := mount(req, nil, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = w.Unmount()
	if err == nil || !strings.Contains(err.Error(), "Resource busy") {
		t.Fatalf("err = %v", err)
	}
	if len(w.mounts) != 1 || w.mounts[0] != req.MergedDir {
		t.Fatalf("mounts = %v, want the view to stay mounted", w.mounts)
	}
	got, err := os.ReadFile(shadow)
	if err != nil || string(got) != "keep" {
		t.Fatalf("shadow = %q, %v", got, err)
	}
	detach, err := container.HdiutilDetachArgv(req.MergedDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 2 || !slices.Equal(fake.calls[1], detach) {
		t.Fatalf("calls = %q", fake.calls)
	}
	if slices.Contains(fake.calls[1], "-force") {
		t.Fatalf("detach used -force: %q", fake.calls[1])
	}
}

func TestDMGDetachRequiresMountpointGone(t *testing.T) {
	req, shadow := dmgFixture(t, false)
	fake := &fakeHdiutil{info: []byte("<string>" + req.MergedDir + "</string>")}
	req.Hdiutil = fake.run
	w, err := mount(req, nil, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = w.Unmount()
	if err == nil || !strings.Contains(err.Error(), "still mounted") {
		t.Fatalf("err = %v", err)
	}
	if len(w.mounts) != 1 {
		t.Fatalf("mounts = %v, want the view to stay mounted", w.mounts)
	}
	info := container.HdiutilInfoArgv()
	if len(fake.calls) != 3 || !slices.Equal(fake.calls[2], info) {
		t.Fatalf("calls = %q", fake.calls)
	}
	got, err := os.ReadFile(shadow)
	if err != nil || string(got) != "keep" {
		t.Fatalf("shadow = %q, %v", got, err)
	}
}

func TestDMGDetachSiblingPathClearsMount(t *testing.T) {
	req, shadow := dmgFixture(t, false)
	req.MergedDir = filepath.Join(filepath.Dir(req.MergedDir), "mnt", "app")
	point := req.MergedDir
	fake := &fakeHdiutil{info: []byte(
		"<string>" + point + "2</string>" +
			"<string>" + point + "/image.dmg</string>" +
			"<string>" + point + ".shadow</string>",
	)}
	req.Hdiutil = fake.run
	w, err := mount(req, nil, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Unmount(); err != nil {
		t.Fatal(err)
	}
	if len(w.mounts) != 0 {
		t.Fatalf("mounts = %v, want unmounted", w.mounts)
	}
	got, err := os.ReadFile(shadow)
	if err != nil || string(got) != "keep" {
		t.Fatalf("shadow = %q, %v", got, err)
	}
}

func TestDMGDetachEscapedMountpointStaysMounted(t *testing.T) {
	req, shadow := dmgFixture(t, false)
	req.MergedDir = filepath.Join(filepath.Dir(req.MergedDir), "mnt", "a&b")
	escaped := strings.ReplaceAll(req.MergedDir, "&", "&amp;")
	fake := &fakeHdiutil{info: []byte("<string>" + escaped + "</string>")}
	req.Hdiutil = fake.run
	w, err := mount(req, nil, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = w.Unmount()
	if err == nil || !strings.Contains(err.Error(), "still mounted") {
		t.Fatalf("err = %v", err)
	}
	if len(w.mounts) != 1 || w.mounts[0] != req.MergedDir {
		t.Fatalf("mounts = %v, want the view to stay mounted", w.mounts)
	}
	got, err := os.ReadFile(shadow)
	if err != nil || string(got) != "keep" {
		t.Fatalf("shadow = %q, %v", got, err)
	}
}

func TestDMGUnmountKeepsShadow(t *testing.T) {
	req, shadow := dmgFixture(t, false)
	fake := &fakeHdiutil{}
	req.Hdiutil = fake.run
	w, err := mount(req, nil, testHooks())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Unmount(); err != nil {
		t.Fatal(err)
	}
	if len(w.mounts) != 0 {
		t.Fatalf("mounts = %v, want unmounted", w.mounts)
	}
	got, err := os.ReadFile(shadow)
	if err != nil || string(got) != "keep" {
		t.Fatalf("shadow = %q, %v", got, err)
	}
	if err := w.Unmount(); err != nil {
		t.Fatalf("second unmount: %v", err)
	}
	if len(fake.calls) != 3 {
		t.Fatalf("calls = %q, want attach, detach, info", fake.calls)
	}
}

func TestDMGUnmountPathKeepsShadow(t *testing.T) {
	req, shadow := dmgFixture(t, false)
	if err := os.MkdirAll(req.RuntimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeHdiutil{}
	fuseRan := false
	removed, err := unmountPath(UnmountRequest{
		Dir: req.MergedDir,
		Table: []Entry{{
			Point: req.MergedDir, FSType: "dmg", Source: req.Full, Upper: shadow,
		}},
		RuntimeDir: req.RuntimeDir,
		Hdiutil:    fake.run,
	}, func(name string, args ...string) error {
		fuseRan = true
		return errors.New(name + " " + strings.Join(args, " "))
	}, hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || fuseRan {
		t.Fatalf("removed=%d fuse=%v calls=%q", removed, fuseRan, fake.calls)
	}
	got, err := os.ReadFile(shadow)
	if err != nil || string(got) != "keep" {
		t.Fatalf("shadow = %q, %v", got, err)
	}
	detach, err := container.HdiutilDetachArgv(req.MergedDir)
	if err != nil {
		t.Fatal(err)
	}
	info := container.HdiutilInfoArgv()
	if len(fake.calls) != 2 || !slices.Equal(fake.calls[0], detach) || !slices.Equal(fake.calls[1], info) {
		t.Fatalf("calls = %q", fake.calls)
	}
}

func TestDMGMountUnavailableOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin workspace calls /usr/bin/hdiutil")
	}
	req, _ := dmgFixture(t, false)
	_, err := mount(req, nil, testHooks())
	if err == nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}
