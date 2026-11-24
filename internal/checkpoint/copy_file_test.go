package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFileDataCloneAndFallback(t *testing.T) {
	for _, cloneWorks := range []bool{true, false} {
		t.Run(map[bool]string{true: "clone", false: "fallback"}[cloneWorks], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "src"), []byte("source"), 0600); err != nil {
				t.Fatal(err)
			}
			src, err := os.Open(filepath.Join(dir, "src"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = src.Close() }()
			dst, err := os.Create(filepath.Join(dir, "dst"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dst.Close() }()
			clone := func(dst, _ *os.File) error {
				if !cloneWorks {
					return errCloneUnsupported
				}
				_, err := dst.WriteString("cloned")
				return err
			}
			if err := copyFileData(dst, src, clone); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(dir, "dst"))
			want := "source"
			if cloneWorks {
				want = "cloned"
			}
			if err != nil || string(body) != want {
				t.Fatalf("got %q, %v", body, err)
			}
		})
	}
}

func TestCopyFileDataDoesNotMaskCloneOrReadErrors(t *testing.T) {
	dir := t.TempDir()
	src, err := os.Create(filepath.Join(dir, "src"))
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.Create(filepath.Join(dir, "dst"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	failure := errors.New("clone failed")
	if err := copyFileData(dst, src, func(*os.File, *os.File) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("clone error = %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if err := copyFileData(dst, src, func(*os.File, *os.File) error { return errCloneUnsupported }); err == nil {
		t.Fatal("closed source was accepted")
	}
}

func TestCopyFileDataReportsDestinationError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	dst, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()
	if err := copyFileData(dst, src, func(*os.File, *os.File) error { return errCloneUnsupported }); err == nil {
		t.Fatal("read-only destination was accepted")
	}
}
