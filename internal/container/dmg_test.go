package container

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestDmgCompressionAndDefault(t *testing.T) {
	f := dmg{}
	if f.Type() != dmgType {
		t.Fatalf("Type() = %q", f.Type())
	}
	if f.DefaultCompression() != "ulfo" {
		t.Fatalf("DefaultCompression() = %q", f.DefaultCompression())
	}
	cases := []struct {
		name string
		ok   bool
	}{
		{name: "", ok: true},
		{name: "ulfo", ok: true},
		{name: "ulmo", ok: true},
		{name: "udro", ok: true},
		{name: "zstd"},
		{name: "xz"},
		{name: "gzip"},
		{name: "lz4"},
		{name: "ULFO"},
		{name: "ulfo "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.AllowsCompression(tc.name); got != tc.ok {
				t.Fatalf("AllowsCompression(%q) = %v", tc.name, got)
			}
		})
	}
}

func TestDmgDetectKoly(t *testing.T) {
	dir := t.TempDir()
	koly := []byte("koly")
	cases := []struct {
		name  string
		size  int
		at    int
		magic []byte
		want  bool
	}{
		{name: "trailer", size: dmgTrailerLen, magic: koly, want: true},
		{name: "trailer after prefix", size: dmgTrailerLen + 88, at: 88, magic: koly, want: true},
		{name: "koly at start", size: dmgTrailerLen + 88, magic: koly},
		{name: "shifted in trailer", size: dmgTrailerLen, at: 1, magic: koly},
		{name: "short", size: dmgTrailerLen - 1, magic: koly},
		{name: "empty"},
		{name: "other magic", size: dmgTrailerLen, magic: []byte("xxxx")},
	}
	f := dmg{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := bytes.Repeat([]byte{'x'}, tc.size)
			copy(body[tc.at:], tc.magic)
			path := filepath.Join(dir, tc.name)
			if err := os.WriteFile(path, body, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := f.Detect(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("Detect() = %v", got)
			}
		})
	}

	got, err := f.Detect(filepath.Join(dir, "missing"))
	if err != nil || got {
		t.Fatalf("missing = %v, %v", got, err)
	}
	got, err = f.Detect(dir)
	if err != nil || got {
		t.Fatalf("directory = %v, %v", got, err)
	}

	unread := filepath.Join(dir, "unread")
	if err := os.WriteFile(unread, bytes.Repeat(koly, dmgTrailerLen/len(koly)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unread, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unread, 0o644) })
	opened, openErr := os.Open(unread)
	if openErr == nil {
		_ = opened.Close()
		t.Skip("this user can read a mode 000 file")
	}
	got, err = f.Detect(unread)
	if err != nil || got {
		t.Fatalf("unreadable = %v, %v", got, err)
	}
	ok, readErr := ReadKolyTrailer(unread)
	if readErr == nil || ok {
		t.Fatalf("ReadKolyTrailer(unreadable) = %v, %v", ok, readErr)
	}
}

func TestDmgToolsMountAndPack(t *testing.T) {
	f := dmg{}
	want := []string{"hdiutil"}
	tools := f.Tools()
	if !slices.Equal(tools, want) {
		t.Fatalf("Tools() = %v", tools)
	}
	tools[0] = "mutated"
	if again := f.Tools(); !slices.Equal(again, want) {
		t.Fatalf("Tools() after mutation = %v", again)
	}
	if argv := f.MountArgv("fs", "sub", "opt", "img", "point"); len(argv) != 0 {
		t.Fatalf("MountArgv = %q", argv)
	}

	runner := &argvRunner{}
	err := f.PackIncr(runner.run, "src", "dst", "ulfo")
	if !errors.Is(err, errDmgNoIncr) || len(runner.calls) != 0 {
		t.Fatalf("PackIncr = %v calls=%d", err, len(runner.calls))
	}
	err = f.PackFull(runner.run, "src", "dst", "ulfo")
	if err == nil || len(runner.calls) != 0 {
		t.Fatalf("PackFull err=%v calls=%d", err, len(runner.calls))
	}
	ok, err := f.HasSubdir(nil, "img", "")
	if err != nil || !ok {
		t.Fatalf("root subdir = %v, %v", ok, err)
	}
	ok, err = f.HasSubdir(runner.run, "img", "src")
	if err == nil || ok || len(runner.calls) != 0 {
		t.Fatalf("subdir = %v, %v calls=%d", ok, err, len(runner.calls))
	}
}

func TestDmgRegistryOmitsHdiutilOffDarwin(t *testing.T) {
	found := false
	for _, format := range All() {
		if format.Type() == dmgType {
			found = true
		}
		if runtime.GOOS == "darwin" {
			continue
		}
		for _, tool := range format.Tools() {
			if tool == "hdiutil" {
				t.Fatalf("%s lists hdiutil", format.Type())
			}
		}
	}
	_, err := Lookup(dmgType)
	if runtime.GOOS == "darwin" {
		if err != nil || !found {
			t.Fatalf("darwin registry err=%v found=%v", err, found)
		}
		return
	}
	if found || !errors.Is(err, ErrUnknownType) {
		t.Fatalf("registry err=%v found=%v", err, found)
	}
	if !strings.Contains(err.Error(), "the format mounts only on macOS") {
		t.Fatalf("registry err=%v", err)
	}
}

func TestLookupUnregisteredDMGMountsOnlyOnMacOS(t *testing.T) {
	isolate(t)
	_, err := Lookup(dmgType)
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("Lookup err = %v", err)
	}
	if !strings.Contains(err.Error(), "the format mounts only on macOS") {
		t.Fatalf("Lookup err = %v", err)
	}
}
