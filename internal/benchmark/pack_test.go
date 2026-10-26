package benchmark

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/container"
)

const (
	packSrc   = "/bench/src"
	packImage = "/bench/app.dmg"
	packPoint = "/bench/mnt"
)

type argvCall struct {
	name string
	env  []string
	args []string
}

func (c argvCall) String() string {
	return fmt.Sprintf("%s env=%q args=%q", c.name, c.env, c.args)
}

type argvRec struct {
	calls   []argvCall
	help    string
	helpErr error
	fail    func(name string, args []string) error
}

func (r *argvRec) command(name string, env []string, args ...string) ([]byte, error) {
	call := argvCall{name: name, env: cloneStrings(env), args: cloneStrings(args)}
	r.calls = append(r.calls, call)
	if name == "mkfs.erofs" && len(args) == 1 && args[0] == "--help" {
		if r.help == "" && r.helpErr != nil {
			return nil, r.helpErr
		}
		return []byte(r.help), r.helpErr
	}
	if r.fail != nil {
		if err := r.fail(name, call.args); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (r *argvRec) runner() container.ToolRunner {
	return func(name string, env []string, args ...string) error {
		_, err := r.command(name, env, args...)
		return err
	}
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

func sameCalls(a, b []argvCall) bool {
	return slices.EqualFunc(a, b, func(x, y argvCall) bool {
		return x.name == y.name && slices.Equal(x.env, y.env) && slices.Equal(x.args, y.args)
	})
}

func TestPackRejectsNilRunner(t *testing.T) {
	err := Pack(nil, "tar", packSrc, "/bench/out.tar.gz", "")
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("Pack error = %v", err)
	}
}

func TestPackDMGMatchesHdiutilCreateArgv(t *testing.T) {
	t.Run("ulfo", func(t *testing.T) {
		assertDMGPack(t, "ulfo")
	})
	t.Run("hfs ulmo", func(t *testing.T) {
		restore := container.SetDMGCreateFilesystem(func() string { return "HFS+" })
		t.Cleanup(restore)
		assertDMGPack(t, "ulmo")
	})
}

func assertDMGPack(t *testing.T, compress string) {
	t.Helper()
	rec := &argvRec{}
	if err := Pack(rec.command, "dmg", packSrc, packImage, compress); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	want, err := container.HdiutilCreateArgv(packSrc, compress, filepath.Base(packImage), packImage)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %v", rec.calls)
	}
	assertHdiutil(t, rec.calls[0], want)
	if !slices.Contains(rec.calls[0].args, container.DMGCreateFilesystem()) {
		t.Fatalf("args = %q, want -fs %s", rec.calls[0].args, container.DMGCreateFilesystem())
	}
}

func assertHdiutil(t *testing.T, got argvCall, argv []string) {
	t.Helper()
	if len(argv) < 2 || got.name != argv[0] || got.env != nil || !slices.Equal(got.args, argv[1:]) {
		t.Fatalf("call = %s, want %q", got, argv)
	}
}

func TestPackSquashfsMatchesPackFull(t *testing.T) {
	assertPackFull(t, "squashfs", "lz4")
}

func TestPackErofsMatchesPackFull(t *testing.T) {
	assertPackFull(t, "erofs", "gzip")
}

func assertPackFull(t *testing.T, typ, compress string) {
	t.Helper()
	rec := &argvRec{}
	if err := Pack(rec.command, typ, packSrc, "/bench/image", compress); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	format, err := container.Lookup(typ)
	if err != nil {
		t.Fatal(err)
	}
	want := &argvRec{}
	err = format.PackFull(want.runner(), packSrc, "/bench/image", compress)
	if err != nil {
		t.Fatal(err)
	}
	if !sameCalls(rec.calls, want.calls) {
		t.Fatalf("calls = %v, want %v", rec.calls, want.calls)
	}
}

func TestPackErofsPicksCompressionFromHelp(t *testing.T) {
	cases := []struct {
		name    string
		help    string
		helpErr error
		want    string
		fail    bool
	}{
		{
			name: "zstd first",
			help: "Available compressors are: lz4, zstd, deflate\n",
			want: "zstd",
		},
		{
			name: "lz4 without zstd",
			help: "Available compressors are: lz4, deflate\n",
			want: "lz4",
		},
		{
			name: "gzip via deflate",
			help: "Available compressors are: deflate\n",
			want: "gzip",
		},
		{
			name:    "nonzero help status",
			help:    "Available compressors are: lz4\n",
			helpErr: errors.New("exit status 1"),
			want:    "lz4",
		},
		{
			name: "no usable compressor",
			help: "Available compressors are: lz4hc\n",
			fail: true,
		},
		{
			name:    "missing tool",
			helpErr: errors.New("executable file not found"),
			fail:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &argvRec{help: tc.help, helpErr: tc.helpErr}
			err := Pack(rec.command, "erofs", packSrc, "/bench/image.erofs", "")
			if tc.fail {
				if err == nil {
					t.Fatal("expected error")
				}
				if len(rec.calls) != 1 || rec.calls[0].args[0] != "--help" {
					t.Fatalf("calls = %v", rec.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("Pack: %v", err)
			}
			format, lookErr := container.Lookup("erofs")
			if lookErr != nil {
				t.Fatal(lookErr)
			}
			want := &argvRec{}
			if err := format.PackFull(want.runner(), packSrc, "/bench/image.erofs", tc.want); err != nil {
				t.Fatal(err)
			}
			if rec.calls[0].name != "mkfs.erofs" || !slices.Equal(rec.calls[0].args, []string{"--help"}) {
				t.Fatalf("help call = %s", rec.calls[0])
			}
			if !sameCalls(rec.calls[1:], want.calls) {
				t.Fatalf("pack calls = %v, want %v", rec.calls[1:], want.calls)
			}
		})
	}
}

func TestPackTarRecordsCreateArgv(t *testing.T) {
	rec := &argvRec{}
	image := "/bench/out.tar.gz"
	if err := Pack(rec.command, "tar", packSrc, image, ""); err != nil {
		t.Fatalf("Pack: %v", err)
	}
	want := []string{"-czf", image, "-C", filepath.Dir(packSrc), filepath.Base(packSrc)}
	if len(rec.calls) != 1 || rec.calls[0].name != "tar" || !slices.Equal(rec.calls[0].args, want) {
		t.Fatalf("calls = %v", rec.calls)
	}
}

func TestPackTarRejectsArchiveInsideSource(t *testing.T) {
	cases := []string{
		packSrc,
		filepath.Join(packSrc, "out.tar.gz"),
		filepath.Join(packSrc, "nested", "out.tar.gz"),
	}
	for _, image := range cases {
		t.Run(image, func(t *testing.T) {
			rec := &argvRec{}
			err := Pack(rec.command, "tar", packSrc, image, "")
			if err == nil || !strings.Contains(err.Error(), "inside") {
				t.Fatalf("Pack error = %v", err)
			}
			if len(rec.calls) != 0 {
				t.Fatalf("calls = %v", rec.calls)
			}
		})
	}
}

func TestPackRejectsUnknownType(t *testing.T) {
	rec := &argvRec{}
	err := Pack(rec.command, "qcow", packSrc, "/bench/out", "zstd")
	if err == nil || !strings.Contains(err.Error(), "unknown image type") {
		t.Fatalf("Pack error = %v", err)
	}
}

func TestReadTarStreamsMembersAndSkipsDotNames(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "image.tar.gz")
	body := []byte("abcd")
	writeGzipTar(t, archive, []tarMember{
		{name: "data/", dir: true},
		{name: "data/f00000", body: body},
		{name: "data/.secret", body: []byte("nope")},
		{name: ".hidden", body: []byte("nope")},
		{name: "data/link", link: "data/f00000"},
	})
	n, elapsed, err := ReadTar(archive)
	if err != nil {
		t.Fatalf("ReadTar: %v", err)
	}
	if n != int64(len(body)) {
		t.Fatalf("bytes = %d, want %d", n, len(body))
	}
	if elapsed < 0 {
		t.Fatalf("elapsed = %s", elapsed)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "image.tar.gz" {
		t.Fatalf("dir entries = %v", entries)
	}
}

func TestReadTarRejectsPlainFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(path, []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadTar(path); err == nil {
		t.Fatal("expected gzip error")
	}
}

type tarMember struct {
	name string
	body []byte
	dir  bool
	link string
}

func writeGzipTar(t *testing.T, path string, members []tarMember) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, member := range members {
		hdr := &tar.Header{Name: member.name, Mode: 0o644, Size: int64(len(member.body))}
		switch {
		case member.dir:
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
		case member.link != "":
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = member.link
			hdr.Size = 0
		default:
			hdr.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(member.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
