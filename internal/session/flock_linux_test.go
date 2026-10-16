//go:build linux

package session

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockSessionSerializesSameSession(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	const workers = 8
	var (
		active    atomic.Int32
		maxActive atomic.Int32
		wg        sync.WaitGroup
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := store.lockSession("dev", 30*time.Second)
			if err != nil {
				t.Errorf("lock: %v", err)
				return
			}
			current := active.Add(1)
			for {
				peak := maxActive.Load()
				if current <= peak || maxActive.CompareAndSwap(peak, current) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			active.Add(-1)
			release()
		}()
	}
	wg.Wait()
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent holders = %d, want 1", got)
	}
}

func TestLockSessionAllowsDifferentSessionsInParallel(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	releaseA, err := store.lockSession("alpha", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	releaseB, err := store.lockSession("zeta", time.Second)
	if err != nil {
		t.Fatalf("a second session blocked behind the first: %v", err)
	}
	releaseB()
}

func TestTryLockSessionReportsHeld(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	release, err := store.lockSession("dev", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.tryLockSession("dev"); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("try error = %v, want ErrLockHeld", err)
	}
	release()
	releaseTry, err := store.tryLockSession("dev")
	if err != nil {
		t.Fatalf("try after release: %v", err)
	}
	releaseTry()
}

func TestLockSessionTimeoutIsRuntimeErrorNotHang(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	release, err := store.lockSession("dev", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := time.Now()
	_, err = store.lockSession("dev", 150*time.Millisecond)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("lock error = %v, want ErrLockTimeout", err)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("the timed lock gave up after %v", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the timed lock hung for %v", elapsed)
	}
}

func TestLockCreatesLockFileBesideRecord(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "dev")
	release, err := store.lockSession("dev", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	release()
	info, err := os.Stat(filepath.Join(dir, "dev.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode = %v, want 0600", info.Mode().Perm())
	}
}
