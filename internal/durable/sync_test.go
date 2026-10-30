package durable

import (
	"errors"
	"testing"
)

type failedFile struct {
	err error
}

func (f failedFile) Sync() error { return f.err }

func TestSyncPreservesFileError(t *testing.T) {
	want := errors.New("sync failed")
	if err := Sync(failedFile{err: want}); !errors.Is(err, want) {
		t.Fatalf("Sync = %v, want %v", err, want)
	}
}
