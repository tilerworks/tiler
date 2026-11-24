//go:build darwin

package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinExchangeFailurePreservesDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "source"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := OpenParent(filepath.Join(root, "source"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	original := renameDarwinAt
	t.Cleanup(func() { renameDarwinAt = original })
	renameDarwinAt = func(int, string, int, string, uint32) error { return unix.ENOTSUP }
	if err := p.Exchange("source", "dest"); !errors.Is(err, unix.ENOTSUP) {
		t.Fatalf("exchange: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "source")); err != nil {
		t.Fatal(err)
	}
	if err := p.Exchange("../source", "dest"); err == nil {
		t.Fatal("accepted unsafe name")
	}
}
