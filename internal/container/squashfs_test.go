package container

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSquashfsRegistered(t *testing.T) {
	got, err := Lookup("squashfs")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type() != "squashfs" {
		t.Fatalf("Type() = %q", got.Type())
	}
	if got.DefaultCompression() != "zstd" {
		t.Fatalf("DefaultCompression() = %q", got.DefaultCompression())
	}
	tools := got.Tools()
	want := []string{"mksquashfs", "squashfuse_ll"}
	if !slices.Equal(tools, want) {
		t.Fatalf("Tools() = %v", tools)
	}
	tools[0] = "mutated"
	if again := got.Tools(); !slices.Equal(again, want) {
		t.Fatalf("Tools() after mutation = %v", again)
	}

	formats := All()
	found := false
	for _, f := range formats {
		if f.Type() == "squashfs" {
			found = true
		}
	}
	if !found {
		t.Fatalf("All() = %v, want squashfs registered", typesOf(formats))
	}
}

func TestSquashfsAllowsCompression(t *testing.T) {
	f := squashfs{}
	cases := []struct {
		name string
		ok   bool
	}{
		{name: "", ok: true},
		{name: "zstd", ok: true},
		{name: "gzip", ok: true},
		{name: "lz4", ok: true},
		{name: "xz", ok: true},
		{name: "lzo"},
		{name: "Zstd"},
		{name: "zstd "},
		{name: "lzma"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.AllowsCompression(tc.name); got != tc.ok {
				t.Fatalf("AllowsCompression(%q) = %v", tc.name, got)
			}
		})
	}
}

func TestSquashfsDetect(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		body []byte
		want bool
	}{
		{name: "little endian", body: []byte("hsqsrest"), want: true},
		{name: "big endian", body: []byte("sqshrest"), want: true},
		{name: "magic only", body: []byte("hsqs"), want: true},
		{name: "short", body: []byte("hsq")},
		{name: "empty"},
		{name: "other magic", body: []byte("nope")},
		{name: "shifted magic", body: []byte("xxhsqs")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			if err := os.WriteFile(path, tc.body, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := squashfs{}.Detect(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("Detect() = %v", got)
			}
		})
	}

	_, err := squashfs{}.Detect(filepath.Join(dir, "missing"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Detect missing error = %v", err)
	}
	_, err = squashfs{}.Detect(dir)
	if err == nil {
		t.Fatal("Detect directory succeeded")
	}
}

func TestSquashfsDetectFile(t *testing.T) {
	dir := t.TempDir()
	hsqs := filepath.Join(dir, "image.sqsh")
	if err := os.WriteFile(hsqs, []byte("hsqspayload"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := DetectFile(hsqs)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type() != "squashfs" {
		t.Fatalf("DetectFile = %s", got.Type())
	}

	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("not-a-container"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = DetectFile(plain)
	if !errors.Is(err, ErrNotDetected) {
		t.Fatalf("DetectFile error = %v, want ErrNotDetected", err)
	}
}

func TestSquashfsMountArgv(t *testing.T) {
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
	f := squashfs{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := f.MountArgv(tc.fsname, tc.subdir, tc.options, tc.path, point)
			want := []string{"squashfuse_ll", "-o", tc.want, tc.path, point}
			if !slices.Equal(got, want) {
				t.Fatalf("argv = %q, want %q", got, want)
			}
		})
	}
}

type argvRunner struct {
	calls []recordedCall
	err   error
}

type recordedCall struct {
	name string
	env  []string
	args []string
}

func (r *argvRunner) run(name string, env []string, args ...string) error {
	r.calls = append(r.calls, recordedCall{
		name: name,
		env:  append([]string(nil), env...),
		args: append([]string(nil), args...),
	})
	return r.err
}

func TestSquashfsPackArgv(t *testing.T) {
	src := squashEpochTree(t, 1700000000)
	dst := filepath.Join(t.TempDir(), "out.sqsh")
	f := squashfs{}
	cases := []struct {
		name string
		incr bool
		comp string
		want []string
	}{
		{
			name: "full default compression",
			want: []string{src, dst, "-comp", "zstd", "-noappend"},
		},
		{
			name: "full gzip",
			comp: "gzip",
			want: []string{src, dst, "-comp", "gzip", "-noappend"},
		},
		{
			name: "incremental keeps xattrs",
			incr: true,
			comp: "lz4",
			want: []string{src, dst, "-comp", "lz4", "-noappend", "-xattrs"},
		},
		{
			name: "incremental xz",
			incr: true,
			comp: "xz",
			want: []string{src, dst, "-comp", "xz", "-noappend", "-xattrs"},
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
			if call.name != "mksquashfs" || !slices.Equal(call.args, tc.want) {
				t.Fatalf("call = %s %q", call.name, call.args)
			}
			wantEnv := []string{"SOURCE_DATE_EPOCH=1700000000"}
			if !slices.Equal(call.env, wantEnv) {
				t.Fatalf("env = %v", call.env)
			}
		})
	}
}

func TestSquashfsPackHonorsSourceDateEpoch(t *testing.T) {
	src := squashEpochTree(t, 1700000000)
	t.Setenv("SOURCE_DATE_EPOCH", "1234567890")
	runner := &argvRunner{}
	dst := filepath.Join(t.TempDir(), "out.sqsh")
	if err := (squashfs{}).PackFull(runner.run, src, dst, "zstd"); err != nil {
		t.Fatal(err)
	}
	want := []string{"SOURCE_DATE_EPOCH=1234567890"}
	if len(runner.calls) != 1 || !slices.Equal(runner.calls[0].env, want) {
		t.Fatalf("env = %v", runner.calls)
	}
}

func TestSquashfsPackRejectsBadInput(t *testing.T) {
	f := squashfs{}
	runner := &argvRunner{err: errors.New("mksquashfs failed")}
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out.sqsh")

	err := f.PackFull(runner.run, src, dst, "lzo")
	if err == nil || len(runner.calls) != 0 {
		t.Fatalf("bad compression err=%v calls=%d", err, len(runner.calls))
	}
	err = f.PackIncr(nil, src, dst, "zstd")
	if !errors.Is(err, errSquashfsRunner) {
		t.Fatalf("nil runner error = %v", err)
	}
	err = f.PackFull(runner.run, src, dst, "gzip")
	if !errors.Is(err, runner.err) {
		t.Fatalf("runner error = %v", err)
	}
}

func TestSquashfsHasSubdir(t *testing.T) {
	const (
		image  = "/images/alice/app/image.sqsh"
		subdir = "src/lib"
	)
	f := squashfs{}

	present := &argvRunner{}
	ok, err := f.HasSubdir(present.run, image, subdir)
	if err != nil || !ok {
		t.Fatalf("present = %v, %v", ok, err)
	}
	if len(present.calls) != 1 {
		t.Fatalf("calls = %d", len(present.calls))
	}
	call := present.calls[0]
	want := []string{"-ls", "-match", image, subdir}
	if call.name != "unsquashfs" || call.env != nil || !slices.Equal(call.args, want) {
		t.Fatalf("call = %s env=%v args=%q", call.name, call.env, call.args)
	}

	absent := &argvRunner{err: errors.New("unsquashfs: Pathname not found")}
	ok, err = f.HasSubdir(absent.run, image, subdir)
	if err != nil || ok {
		t.Fatalf("absent = %v, %v", ok, err)
	}

	ok, err = f.HasSubdir(nil, image, "")
	if err != nil || !ok {
		t.Fatalf("empty subdir = %v, %v", ok, err)
	}
	_, err = f.HasSubdir(nil, image, subdir)
	if !errors.Is(err, errSquashfsRunner) {
		t.Fatalf("nil runner error = %v", err)
	}
}

// squashEpochTree is a source tree whose newest mtime is the given
// Unix second, with any inherited SOURCE_DATE_EPOCH cleared so the
// derivation path runs.
func squashEpochTree(t *testing.T, unix int64) string {
	t.Helper()
	t.Setenv("SOURCE_DATE_EPOCH", "")
	src := t.TempDir()
	file := filepath.Join(src, "a.txt")
	if err := os.WriteFile(file, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(unix, 0)
	if err := os.Chtimes(file, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(src, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return src
}

func TestSourceDateEpochEdges(t *testing.T) {
	src := squashEpochTree(t, 1700000000)
	if got := sourceDateEpoch(src); got != 1700000000 {
		t.Fatalf("epoch = %d", got)
	}
	t.Setenv("SOURCE_DATE_EPOCH", "not-a-number")
	if got := sourceDateEpoch(src); got != 1700000000 {
		t.Fatalf("unparsable epoch = %d", got)
	}
	t.Setenv("SOURCE_DATE_EPOCH", "1234567890")
	if got := sourceDateEpoch(src); got != 1234567890 {
		t.Fatalf("env epoch = %d", got)
	}
	t.Setenv("SOURCE_DATE_EPOCH", "")
	if got := sourceDateEpoch(filepath.Join(t.TempDir(), "absent")); got != 0 {
		t.Fatalf("missing tree epoch = %d", got)
	}
}
