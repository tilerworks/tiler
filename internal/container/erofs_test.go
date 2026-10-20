package container

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestErofsCompressionFollowsTheTool(t *testing.T) {
	help := "Available compressors are: lz4, lz4hc, lzma, deflate, libdeflate\nmkfs.erofs 1.7.1\n"
	listed := ErofsCompressors(help)
	if !slices.Equal(listed, []string{"lz4", "lz4hc", "lzma", "deflate", "libdeflate"}) {
		t.Fatalf("compressors = %q", listed)
	}
	if ErofsCompressors("no such line") != nil {
		t.Fatal("missing line should yield nil")
	}
	if ErofsToolSupports("zstd", listed) || !ErofsToolSupports("lz4", listed) || !ErofsToolSupports("gzip", listed) {
		t.Fatalf("support = zstd %v lz4 %v gzip %v",
			ErofsToolSupports("zstd", listed),
			ErofsToolSupports("lz4", listed),
			ErofsToolSupports("gzip", listed))
	}
	if ErofsToolSupports("lz4", []string{"lz4hc"}) {
		t.Fatal("lz4hc must not satisfy lz4")
	}
	picked, err := PickErofsCompression(listed)
	if err != nil || picked != "lz4" {
		t.Fatalf("pick = %q, %v", picked, err)
	}
	picked, err = PickErofsCompression([]string{"zstd", "lz4"})
	if err != nil || picked != "zstd" {
		t.Fatalf("zstd pick = %q, %v", picked, err)
	}
	picked, err = PickErofsCompression([]string{"libdeflate"})
	if err != nil || picked != "gzip" {
		t.Fatalf("gzip pick = %q, %v", picked, err)
	}
	if _, err := PickErofsCompression([]string{"lzma"}); err == nil {
		t.Fatal("lzma alone should not be usable")
	}
}

func TestErofsRegistered(t *testing.T) {
	got, err := Lookup("erofs")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type() != "erofs" {
		t.Fatalf("Type() = %q", got.Type())
	}
	if got.DefaultCompression() != "zstd" {
		t.Fatalf("DefaultCompression() = %q", got.DefaultCompression())
	}
	tools := got.Tools()
	want := []string{"mkfs.erofs", "erofsfuse"}
	if !slices.Equal(tools, want) {
		t.Fatalf("Tools() = %v", tools)
	}
	tools[0] = "mutated"
	if again := got.Tools(); !slices.Equal(again, want) {
		t.Fatalf("Tools() after mutation = %v", again)
	}

	found := false
	for _, f := range All() {
		if f.Type() == "erofs" {
			found = true
		}
	}
	if !found {
		t.Fatal("All() does not include erofs")
	}
}

func TestErofsAllowsCompression(t *testing.T) {
	f := erofs{}
	cases := []struct {
		name string
		ok   bool
	}{
		{name: "", ok: true},
		{name: "zstd", ok: true},
		{name: "lz4", ok: true},
		{name: "gzip", ok: true},
		{name: "xz"},
		{name: "deflate"},
		{name: "lzma"},
		{name: "lzo"},
		{name: "Gzip"},
		{name: "gzip "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.AllowsCompression(tc.name); got != tc.ok {
				t.Fatalf("AllowsCompression(%q) = %v", tc.name, got)
			}
		})
	}
}

func TestErofsDetect(t *testing.T) {
	dir := t.TempDir()
	const magic = "\xe2\xe1\xf5\xe0"
	cases := []struct {
		name   string
		body   []byte
		offset int
		want   bool
	}{
		{name: "little endian", body: []byte(magic), offset: erofsMagicOffset, want: true},
		{name: "magic and tail", body: []byte(magic + "rest"), offset: erofsMagicOffset, want: true},
		{name: "big endian", body: []byte("\xe0\xf5\xe1\xe2"), offset: erofsMagicOffset},
		{name: "magic at start", body: []byte(magic), offset: 0},
		{name: "short of superblock", body: []byte(magic), offset: erofsMagicOffset - 1},
		{name: "three magic bytes", body: []byte(magic[:3]), offset: erofsMagicOffset},
		{name: "empty"},
		{name: "other magic", body: []byte("nope"), offset: erofsMagicOffset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeAt(t, dir, tc.name, tc.offset, tc.body)
			got, err := erofs{}.Detect(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("Detect() = %v", got)
			}
		})
	}

	_, err := erofs{}.Detect(filepath.Join(dir, "missing"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Detect missing error = %v", err)
	}
	_, err = erofs{}.Detect(dir)
	if err == nil {
		t.Fatal("Detect directory succeeded")
	}
}

func TestErofsDetectFile(t *testing.T) {
	dir := t.TempDir()
	image := writeAt(t, dir, "image.sqsh", erofsMagicOffset, []byte{0xE2, 0xE1, 0xF5, 0xE0})
	got, err := DetectFile(image)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type() != "erofs" {
		t.Fatalf("DetectFile = %s", got.Type())
	}

	both := writeAt(t, dir, "both", 0, []byte("hsqs"))
	if err := overlayAt(both, erofsMagicOffset, []byte{0xE2, 0xE1, 0xF5, 0xE0}); err != nil {
		t.Fatal(err)
	}
	_, err = DetectFile(both)
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("DetectFile error = %v, want ErrAmbiguous", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "erofs") || !strings.Contains(msg, "squashfs") {
		t.Fatalf("DetectFile error = %q, want both types", msg)
	}
}

func TestErofsMountArgv(t *testing.T) {
	const (
		orig   = "/images/alice/app/image.sqsh"
		cached = "/cache/4f2a9c.sqsh"
		point  = "/runtime/up-1-2/lower-0"
	)
	cases := []struct {
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
			name:   "subdir loads the fuse module",
			fsname: orig, subdir: "src/lib", path: orig,
			want: "fsname=" + orig + ",modules=subdir,subdir=src/lib",
		},
		{
			name:   "format options are ignored",
			fsname: orig, options: "allow_other", path: cached,
			want: "fsname=" + orig,
		},
		{
			name:   "subdir stays when options are set",
			fsname: orig, subdir: "src", options: "allow_other", path: orig,
			want: "fsname=" + orig + ",modules=subdir,subdir=src",
		},
	}
	f := erofs{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := f.MountArgv(tc.fsname, tc.subdir, tc.options, tc.path, point)
			want := []string{"erofsfuse", "-o", tc.want, tc.path, point}
			if !slices.Equal(got, want) {
				t.Fatalf("argv = %q, want %q", got, want)
			}
		})
	}
}

func TestErofsPackArgv(t *testing.T) {
	src := squashEpochTree(t, 1700000000)
	dst := filepath.Join(t.TempDir(), "out.sqsh")
	f := erofs{}
	cases := []struct {
		name string
		incr bool
		comp string
		want []string
	}{
		{
			name: "full default compression",
			want: []string{"-U", erofsNilUUID, "-zzstd", dst, src},
		},
		{
			name: "full gzip maps to deflate",
			comp: "gzip",
			want: []string{"-U", erofsNilUUID, "-zdeflate", dst, src},
		},
		{
			name: "full lz4",
			comp: "lz4",
			want: []string{"-U", erofsNilUUID, "-zlz4", dst, src},
		},
		{
			name: "incremental keeps the same command",
			incr: true,
			comp: "zstd",
			want: []string{"-U", erofsNilUUID, "-zzstd", dst, src},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &argvRunner{}
			var err error
			if tc.incr {
				err = f.PackIncr(runner.run, src, dst, tc.comp)
			} else {
				err = f.PackFull(runner.run, src, dst, tc.comp)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != 1 {
				t.Fatalf("calls = %d", len(runner.calls))
			}
			call := runner.calls[0]
			if call.name != "mkfs.erofs" || !slices.Equal(call.args, tc.want) {
				t.Fatalf("call = %s %q", call.name, call.args)
			}
			wantEnv := []string{"SOURCE_DATE_EPOCH=1700000000"}
			if !slices.Equal(call.env, wantEnv) {
				t.Fatalf("env = %v", call.env)
			}
		})
	}
}

func TestErofsPackHonorsSourceDateEpoch(t *testing.T) {
	src := squashEpochTree(t, 1700000000)
	t.Setenv("SOURCE_DATE_EPOCH", "1234567890")
	runner := &argvRunner{}
	dst := filepath.Join(t.TempDir(), "out.sqsh")
	if err := (erofs{}).PackFull(runner.run, src, dst, ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"SOURCE_DATE_EPOCH=1234567890"}
	if len(runner.calls) != 1 || !slices.Equal(runner.calls[0].env, want) {
		t.Fatalf("env = %v", runner.calls)
	}
	if !slices.Equal(runner.calls[0].args, []string{"-U", erofsNilUUID, "-zzstd", dst, src}) {
		t.Fatalf("args = %q", runner.calls[0].args)
	}
}

func TestErofsPackRejectsBadInput(t *testing.T) {
	f := erofs{}
	runner := &argvRunner{err: errors.New("mkfs.erofs failed")}
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out.sqsh")

	err := f.PackFull(runner.run, src, dst, "xz")
	if err == nil || len(runner.calls) != 0 {
		t.Fatalf("xz err=%v calls=%d", err, len(runner.calls))
	}
	if !strings.Contains(err.Error(), `"xz"`) {
		t.Fatalf("xz error = %q", err)
	}
	err = f.PackIncr(runner.run, src, dst, "lzo")
	if err == nil || len(runner.calls) != 0 {
		t.Fatalf("lzo err=%v calls=%d", err, len(runner.calls))
	}
	err = f.PackIncr(nil, src, dst, "zstd")
	if !errors.Is(err, errErofsRunner) {
		t.Fatalf("nil runner error = %v", err)
	}
	err = f.PackFull(runner.run, src, dst, "gzip")
	if !errors.Is(err, runner.err) {
		t.Fatalf("runner error = %v", err)
	}
}

func TestErofsHasSubdir(t *testing.T) {
	const (
		image  = "/images/alice/app/image.sqsh"
		subdir = "src/lib"
	)
	f := erofs{}

	present := &erofsProbe{onMount: func(point string) error {
		return os.MkdirAll(filepath.Join(point, subdir), 0o755)
	}}
	ok, err := f.HasSubdir(present.run, image, subdir)
	if err != nil || !ok {
		t.Fatalf("present = %v, %v", ok, err)
	}
	point := assertErofsProbe(t, present.calls, image)
	if _, statErr := os.Stat(point); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("mount point %s still exists: %v", point, statErr)
	}

	absent := &erofsProbe{}
	ok, err = f.HasSubdir(absent.run, image, subdir)
	if err != nil || ok {
		t.Fatalf("absent = %v, %v", ok, err)
	}
	assertErofsProbe(t, absent.calls, image)

	file := &erofsProbe{onMount: func(point string) error {
		return os.WriteFile(filepath.Join(point, "src"), []byte("x"), 0o644)
	}}
	ok, err = f.HasSubdir(file.run, image, "src")
	if err != nil || ok {
		t.Fatalf("file = %v, %v", ok, err)
	}

	intermediate := &erofsProbe{onMount: func(point string) error {
		return os.WriteFile(filepath.Join(point, "src"), []byte("x"), 0o644)
	}}
	ok, err = f.HasSubdir(intermediate.run, image, "src/lib")
	if err != nil || ok {
		t.Fatalf("intermediate file = %v, %v", ok, err)
	}

	hostDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(hostDir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	escapedDir := &erofsProbe{onMount: func(point string) error {
		return os.Symlink(hostDir, filepath.Join(point, "src"))
	}}
	ok, err = f.HasSubdir(escapedDir.run, image, "src/lib")
	if err == nil || ok || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("symlink to host directory = %v, %v", ok, err)
	}

	hostFile := filepath.Join(t.TempDir(), "notdir")
	if err := os.WriteFile(hostFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	escapedFile := &erofsProbe{onMount: func(point string) error {
		return os.Symlink(hostFile, filepath.Join(point, "src"))
	}}
	ok, err = f.HasSubdir(escapedFile.run, image, "src")
	if err == nil || ok || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("symlink to host file = %v, %v", ok, err)
	}

	normalized := &erofsProbe{onMount: func(point string) error {
		return os.MkdirAll(filepath.Join(point, "lib"), 0o755)
	}}
	ok, err = f.HasSubdir(normalized.run, image, "src/../lib")
	if err != nil || !ok {
		t.Fatalf("normalized = %v, %v", ok, err)
	}

	for _, root := range []string{"", ".", "foo/.."} {
		ok, err = f.HasSubdir(nil, image, root)
		if err != nil || !ok {
			t.Fatalf("root %q = %v, %v", root, ok, err)
		}
	}

	_, err = f.HasSubdir(nil, image, subdir)
	if !errors.Is(err, errErofsRunner) {
		t.Fatalf("nil runner error = %v", err)
	}
	for _, bad := range []string{"../etc", "/etc", "foo/../../etc"} {
		probe := &erofsProbe{}
		_, err = f.HasSubdir(probe.run, image, bad)
		if err == nil || len(probe.calls) != 0 {
			t.Fatalf("subdir %q err=%v calls=%d", bad, err, len(probe.calls))
		}
	}
}

func TestErofsHasSubdirMountAndUnmountErrors(t *testing.T) {
	const (
		image  = "/images/alice/app/image.sqsh"
		subdir = "src/lib"
	)
	f := erofs{}

	mountFail := &erofsProbe{mountErr: errors.New("erofsfuse: boom")}
	ok, err := f.HasSubdir(mountFail.run, image, subdir)
	if err == nil || ok || len(mountFail.calls) != 1 {
		t.Fatalf("mount failure = %v, %v calls=%d", ok, err, len(mountFail.calls))
	}
	if mountFail.calls[0].name != "erofsfuse" {
		t.Fatalf("mount call = %s", mountFail.calls[0].name)
	}
	mountPoint := mountFail.calls[0].args[len(mountFail.calls[0].args)-1]
	if _, statErr := os.Stat(mountPoint); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("mount failure left %s: %v", mountPoint, statErr)
	}

	unmountFail := &erofsProbe{
		onMount: func(point string) error {
			return os.MkdirAll(filepath.Join(point, subdir), 0o755)
		},
		unmountErr: errors.New("fusermount3: busy"),
	}
	ok, err = f.HasSubdir(unmountFail.run, image, subdir)
	point := unmountFail.calls[0].args[len(unmountFail.calls[0].args)-1]
	t.Cleanup(func() { _ = os.RemoveAll(point) })
	if err == nil || ok || !strings.Contains(err.Error(), "busy") || !strings.Contains(err.Error(), point) {
		t.Fatalf("unmount failure = %v, %v", ok, err)
	}
	if _, statErr := os.Stat(point); statErr != nil {
		t.Fatalf("unmount failure removed %s: %v", point, statErr)
	}
	if len(unmountFail.calls) != 2 || unmountFail.calls[1].name != "fusermount3" {
		t.Fatalf("calls = %+v", unmountFail.calls)
	}

	both := &erofsProbe{
		onMount: func(point string) error {
			return os.Symlink("/", filepath.Join(point, "src"))
		},
		unmountErr: errors.New("fusermount3: busy"),
	}
	ok, err = f.HasSubdir(both.run, image, subdir)
	bothPoint := both.calls[0].args[len(both.calls[0].args)-1]
	t.Cleanup(func() { _ = os.RemoveAll(bothPoint) })
	if err == nil || ok {
		t.Fatalf("stat and unmount failure = %v, %v", ok, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "escapes") || !strings.Contains(msg, "busy") || !strings.Contains(msg, bothPoint) {
		t.Fatalf("stat and unmount failure = %q", msg)
	}
	if _, statErr := os.Stat(bothPoint); statErr != nil {
		t.Fatalf("combined failure removed %s: %v", bothPoint, statErr)
	}
}

// erofsProbe is a ToolRunner that records calls and can materialize
// a tree at the temporary mount point. Stat in HasSubdir is real, so
// a present subdir is a directory the mount callback creates.
type erofsProbe struct {
	argvRunner
	onMount    func(point string) error
	mountErr   error
	unmountErr error
}

func (p *erofsProbe) run(name string, env []string, args ...string) error {
	if err := p.argvRunner.run(name, env, args...); err != nil {
		return err
	}
	switch name {
	case "erofsfuse":
		if p.mountErr != nil {
			return p.mountErr
		}
		if p.onMount == nil {
			return nil
		}
		if len(args) == 0 {
			return errors.New("erofsfuse: missing mount point")
		}
		return p.onMount(args[len(args)-1])
	case "fusermount3":
		return p.unmountErr
	default:
		return fmt.Errorf("unexpected tool %s", name)
	}
}

func assertErofsProbe(t *testing.T, calls []recordedCall, image string) string {
	t.Helper()
	if len(calls) != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	mount := calls[0]
	wantMount := []string{"-o", "ro", image}
	if mount.name != "erofsfuse" || mount.env != nil || len(mount.args) != 4 ||
		!slices.Equal(mount.args[:3], wantMount) {
		t.Fatalf("mount = %s env=%v args=%q", mount.name, mount.env, mount.args)
	}
	point := mount.args[3]
	unmount := calls[1]
	wantUnmount := []string{"-u", point}
	if unmount.name != "fusermount3" || unmount.env != nil || !slices.Equal(unmount.args, wantUnmount) {
		t.Fatalf("unmount = %s env=%v args=%q", unmount.name, unmount.env, unmount.args)
	}
	return point
}

// writeAt writes body at offset, zero-filling the gap in front.
func writeAt(t *testing.T, dir, name string, offset int, body []byte) string {
	t.Helper()
	buf := make([]byte, offset+len(body))
	copy(buf[offset:], body)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// overlayAt writes body at offset without shrinking an existing file.
func overlayAt(path string, offset int, body []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = file.WriteAt(body, int64(offset))
	return err
}
