package container

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Compile-time check that the registry fake implements Format.
var _ Format = (*fakeFormat)(nil)

// fakeFormat is a Format stand-in. Registry tests never pack or mount.
type fakeFormat struct {
	typ         string
	compression string
	allowed     map[string]bool
	detect      func(path string) (bool, error)
	tools       []string
}

func (f *fakeFormat) Type() string { return f.typ }

func (f *fakeFormat) AllowsCompression(compression string) bool {
	if compression == "" {
		return true
	}
	return f.allowed[compression]
}

func (f *fakeFormat) DefaultCompression() string { return f.compression }

func (f *fakeFormat) Detect(path string) (bool, error) {
	if f.detect == nil {
		return false, nil
	}
	return f.detect(path)
}

func (f *fakeFormat) PackFull(ToolRunner, string, string, string) error { return nil }

func (f *fakeFormat) PackIncr(ToolRunner, string, string, string) error { return nil }

func (f *fakeFormat) MountArgv(fsname, subdir, options, imagePath, point string) []string {
	return []string{fsname, subdir, options, imagePath, point}
}

func (f *fakeFormat) HasSubdir(ToolRunner, string, string) (bool, error) {
	return false, nil
}

func (f *fakeFormat) Tools() []string { return append([]string(nil), f.tools...) }

func isolate(t *testing.T) {
	t.Helper()
	mu.Lock()
	savedTypes := byType
	savedOrder := order
	byType = map[string]Format{}
	order = nil
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		byType = savedTypes
		order = savedOrder
		mu.Unlock()
	})
}

func mustRegister(t *testing.T, f Format) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("Register panicked: %v", recovered)
		}
	}()
	Register(f)
}

func TestEmptyRegistryLookupDetectAndAll(t *testing.T) {
	isolate(t)

	if got := All(); len(got) != 0 {
		t.Fatalf("All() = %d formats, want empty", len(got))
	}
	_, err := Lookup("squashfs")
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("Lookup error = %v, want ErrUnknownType", err)
	}
	if !strings.Contains(err.Error(), `"squashfs"`) {
		t.Fatalf("Lookup error = %q, want the quoted type", err)
	}
	_, err = DetectFile("missing.img")
	if !errors.Is(err, ErrNotDetected) {
		t.Fatalf("DetectFile error = %v, want ErrNotDetected", err)
	}
	if !strings.Contains(err.Error(), "registry is empty") {
		t.Fatalf("DetectFile error = %q, want empty-registry detail", err)
	}
	if !strings.Contains(err.Error(), `"missing.img"`) {
		t.Fatalf("DetectFile error = %q, want the quoted path", err)
	}
}

func TestLookupUnknownType(t *testing.T) {
	isolate(t)
	mustRegister(t, &fakeFormat{typ: "alpha", compression: "zstd"})

	_, err := Lookup("beta")
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("Lookup error = %v, want ErrUnknownType", err)
	}
	if !strings.Contains(err.Error(), `"beta"`) {
		t.Fatalf("Lookup error = %q, want the quoted type", err)
	}
	_, err = Lookup("")
	if !errors.Is(err, ErrUnknownType) {
		t.Fatalf("Lookup empty error = %v, want ErrUnknownType", err)
	}

	got, err := Lookup("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type() != "alpha" || got.DefaultCompression() != "zstd" {
		t.Fatalf("Lookup = %s/%s", got.Type(), got.DefaultCompression())
	}
}

func TestRegisterLookupAndAll(t *testing.T) {
	isolate(t)
	alpha := &fakeFormat{typ: "alpha", tools: []string{"pack-a", "mount-a"}}
	beta := &fakeFormat{typ: "beta", tools: []string{"pack-b"}}
	mustRegister(t, alpha)
	mustRegister(t, beta)

	gotAlpha, err := Lookup("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if gotAlpha != alpha {
		t.Fatal("Lookup(alpha) returned a different format")
	}
	formats := All()
	if len(formats) != 2 || formats[0] != alpha || formats[1] != beta {
		t.Fatalf("All() = %v", typesOf(formats))
	}

	formats[0] = nil
	extended := append(formats, beta)
	if len(extended) != 3 || extended[0] != nil {
		t.Fatalf("local All() mutation len=%d first=%v", len(extended), extended[0])
	}
	again := All()
	if len(again) != 2 || again[0] != alpha || again[1] != beta {
		t.Fatalf("All() after caller mutation = %v", typesOf(again))
	}
	if got := alpha.Tools(); len(got) != 2 || got[0] != "pack-a" {
		t.Fatalf("Tools() = %v", got)
	}
}

func TestDetectFileSelectsMatchingFormat(t *testing.T) {
	isolate(t)
	alpha := &fakeFormat{
		typ: "alpha",
		detect: func(path string) (bool, error) {
			return strings.HasSuffix(path, ".a"), nil
		},
	}
	beta := &fakeFormat{
		typ: "beta",
		detect: func(path string) (bool, error) {
			return strings.HasSuffix(path, ".b"), nil
		},
	}
	mustRegister(t, alpha)
	mustRegister(t, beta)

	got, err := DetectFile("layer.b")
	if err != nil {
		t.Fatal(err)
	}
	if got != beta {
		t.Fatalf("DetectFile = %s, want beta", got.Type())
	}
	_, err = DetectFile("plain")
	if !errors.Is(err, ErrNotDetected) {
		t.Fatalf("DetectFile error = %v, want ErrNotDetected", err)
	}
	if strings.Contains(err.Error(), "registry is empty") {
		t.Fatalf("DetectFile error = %q, want a no-match detail", err)
	}
}

func TestDetectFilePropagatesDetectError(t *testing.T) {
	isolate(t)
	sentinel := errors.New("read magic")
	mustRegister(t, &fakeFormat{
		typ: "alpha",
		detect: func(string) (bool, error) {
			return false, sentinel
		},
	})
	var later bool
	mustRegister(t, &fakeFormat{
		typ: "beta",
		detect: func(string) (bool, error) {
			later = true
			return true, nil
		},
	})

	_, err := DetectFile("img")
	if !errors.Is(err, sentinel) {
		t.Fatalf("DetectFile error = %v, want the Detect error", err)
	}
	if later {
		t.Fatal("DetectFile kept probing after a Detect error")
	}
	if !strings.Contains(err.Error(), "alpha") {
		t.Fatalf("DetectFile error = %q, want the format type", err)
	}
}

func TestDetectFileRejectsAmbiguousMatch(t *testing.T) {
	isolate(t)
	mustRegister(t, &fakeFormat{
		typ: "alpha",
		detect: func(string) (bool, error) {
			return true, nil
		},
	})
	mustRegister(t, &fakeFormat{
		typ: "beta",
		detect: func(string) (bool, error) {
			return true, nil
		},
	})

	_, err := DetectFile("both.img")
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("DetectFile error = %v, want ErrAmbiguous", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"both.img"`) || !strings.Contains(msg, "alpha") || !strings.Contains(msg, "beta") {
		t.Fatalf("DetectFile error = %q, want path and both types", msg)
	}
}

func TestRegisterPanicsOnNilEmptyAndDuplicate(t *testing.T) {
	isolate(t)
	expectPanic(t, "nil format", func() { Register(nil) })
	expectPanic(t, "empty type", func() { Register(&fakeFormat{}) })
	mustRegister(t, &fakeFormat{typ: "alpha"})
	expectPanic(t, "duplicate format alpha", func() {
		Register(&fakeFormat{typ: "alpha"})
	})
	if got := All(); len(got) != 1 || got[0].Type() != "alpha" {
		t.Fatalf("All() after rejected registers = %v", typesOf(got))
	}
}

func expectPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("expected panic %q", want)
		}
		if !strings.Contains(fmt.Sprint(recovered), want) {
			t.Fatalf("panic = %v, want %q", recovered, want)
		}
	}()
	fn()
}

func typesOf(formats []Format) []string {
	out := make([]string, len(formats))
	for i, f := range formats {
		if f == nil {
			out[i] = "<nil>"
			continue
		}
		out[i] = f.Type()
	}
	return out
}
