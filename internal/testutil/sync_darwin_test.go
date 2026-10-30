//go:build darwin

package testutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/durable"
)

type failedFile struct {
	err error
}

func (f failedFile) Sync() error { return f.err }

func TestFixtureSyncPreservesFailures(t *testing.T) {
	want := errors.New("injected sync failure")
	if err := durable.Sync(failedFile{err: want}); !errors.Is(err, want) {
		t.Fatalf("Sync = %v, want %v", err, want)
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if err := durable.Sync(f); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := durable.Sync(f); err == nil {
		t.Fatal("syncing a closed file succeeded")
	}
}
