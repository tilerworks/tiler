//go:build linux

package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/jiangxin/tiler/internal/filelock"
)

func TestParentRenameAndLock(t *testing.T) {
	root := t.TempDir()
	p, err := OpenParent(filepath.Join(root, "source"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	for _, name := range []string{"source", "dest"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.RenameNoReplace("source", "dest"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("rename = %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "dest"))
	if err != nil || string(body) != "dest" {
		t.Fatalf("destination changed: %q %v", body, err)
	}
	lock, err := p.Lock("lock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if _, err := p.Lock("lock"); !errors.Is(err, filelock.ErrHeld) {
		t.Fatalf("second lock: %v", err)
	}
	if err := os.Symlink("dest", filepath.Join(root, "badlock")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Lock("badlock"); err == nil {
		t.Fatal("accepted symlink lock")
	}
}

func TestExchangeDirectoriesKeepsPathsAndOpenHandles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	p, err := OpenParent(filepath.Join(root, "first"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	first, err := os.Open(filepath.Join(root, "first"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	before, err := first.Stat()
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	missing := make(chan error, 1)
	ready := make(chan struct{})
	exchanging := make(chan struct{})
	observed := make(chan struct{}, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, e := os.Stat(filepath.Join(root, "first")); e != nil {
			missing <- e
			close(ready)
			return
		}
		close(ready)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, e := os.Stat(filepath.Join(root, "first")); e != nil {
				missing <- e
				return
			}
			select {
			case <-exchanging:
				select {
				case observed <- struct{}{}:
				default:
				}
			default:
			}
		}
	}()
	<-ready
	select {
	case err = <-missing:
		close(stop)
		wg.Wait()
		t.Fatalf("initial path observation: %v", err)
	default:
	}
	for i := 0; i < 101; i++ {
		if err = p.Exchange("first", "second"); err != nil {
			break
		}
		if i == 0 {
			close(exchanging)
			select {
			case <-observed:
			case err = <-missing:
			}
			if err != nil {
				break
			}
		}
	}
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-missing:
		t.Fatalf("path disappeared: %v", err)
	default:
	}
	after, err := os.Stat(filepath.Join(root, "second"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("open directory identity not retained: %v", err)
	}
}

func TestExchangeFailureDoesNotChangeEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "first"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := OpenParent(filepath.Join(root, "first"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if err = p.Exchange("first", "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if err = p.exchangeWith("first", "second", func(int, string, int, string, uint) error { return syscall.ENOSYS }); !errors.Is(err, syscall.ENOSYS) {
		t.Fatalf("unsupported: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "first")); err != nil {
		t.Fatal(err)
	}
	if err = p.Exchange("../first", "second"); err == nil {
		t.Fatal("accepted unsafe name")
	}
}

func TestParentDetectsReplacement(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "parent")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := OpenParent(filepath.Join(dir, "target"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := p.Verify(); err == nil {
		t.Fatal("accepted replaced parent")
	}
}

func TestLockRejectsReplacementDuringAcquisition(t *testing.T) {
	root := t.TempDir()
	p, err := OpenParent(filepath.Join(root, "target"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	lock, err := p.lockWith("lock", func(f *os.File) error {
		if err := os.Rename(filepath.Join(root, "lock"), filepath.Join(root, "old-lock")); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, "lock"), nil, 0600); err != nil {
			return err
		}
		return filelock.Acquire(f, 0)
	})
	if err == nil {
		_ = lock.Close()
		t.Fatal("accepted replaced lock")
	}
}
