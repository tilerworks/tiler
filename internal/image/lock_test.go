package image

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/filelock"
)

func TestLibraryLockCoordinatesContentionAndRelease(t *testing.T) {
	dir := t.TempDir()
	release, err := LockLibrary(dir, 0)
	if errors.Is(err, filelock.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	for _, timeout := range []time.Duration{0, time.Nanosecond} {
		_, err := LockLibrary(dir, timeout)
		want := filelock.ErrHeld
		if timeout > 0 {
			want = filelock.ErrTimeout
		}
		if !errors.Is(err, want) {
			t.Fatalf("timeout %v: %v, want %v", timeout, err, want)
		}
	}
	other, err := LockLibrary(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("independent library: %v", err)
	}
	other()
	release()
	again, err := LockLibrary(dir, 0)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
	info, err := os.Stat(filepath.Join(dir, ".library.lock"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock permissions: %v, %v", info, err)
	}
}

func TestLibraryLockRejectsEmptyRoot(t *testing.T) {
	if _, err := LockLibrary(" ", 0); !errors.Is(err, ErrInvalidPath) {
		t.Fatal(err)
	}
}
