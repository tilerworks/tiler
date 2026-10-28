//go:build linux || darwin

package session

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/filelock"
	"github.com/jiangxin/tiler/internal/image"
)

func newReferenceCoordinator(t *testing.T) ReferenceCoordinator {
	t.Helper()
	c := ReferenceCoordinator{Store: Store{Dir: t.TempDir()}, ImagesDir: t.TempDir(), Timeout: -1}
	if err := c.SaveReferenceState(ReferenceState{Version: ReferenceControlVersion, CatalogComplete: true}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestReferenceCoordinatorCreatesUpdatesAndRemoves(t *testing.T) {
	c := newReferenceCoordinator(t)
	before := versionRecord(t)
	if err := c.Apply(nil, &before); err != nil {
		t.Fatal(err)
	}
	after := before
	after.State = StateClosed
	if err := c.Apply(&before, &after); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(&before, nil); !errors.Is(err, ErrReferenceConflict) {
		t.Fatalf("stale source accepted: %v", err)
	}
	if state := mustReferenceState(t, c.Store); state.Generation != 2 || state.Dirty {
		t.Fatalf("conflict changed state: %#v", state)
	}
	if err := c.Apply(&after, nil); err != nil {
		t.Fatal(err)
	}
	assertReferenceReplayComplete(t, c.Store, ReferenceJournal{Version: ReferenceControlVersion, Generation: 3, Before: &after})
}

func TestReferenceCoordinatorRecoversPendingSessionBeforeNewRequest(t *testing.T) {
	c := newReferenceCoordinator(t)
	pending := versionRecord(t)
	pending.Name = "pending"
	journal := ReferenceJournal{Version: ReferenceControlVersion, Generation: 1, After: &pending}
	if err := c.SaveReferenceJournal(journal); err != nil {
		t.Fatal(err)
	}
	release, err := c.lockSession(pending.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	requested := versionRecord(t)
	err = c.Apply(nil, &requested)
	release()
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("pending lock bypassed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Dir, requested.Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("requested session touched before recovery: %v", err)
	}
	if err := c.Apply(nil, &requested); err != nil {
		t.Fatal(err)
	}
	got, err := c.LoadVersion(pending.Name)
	if err != nil || !sameReferenceRecord(&got, &pending) || mustReferenceState(t, c.Store).Generation != 2 {
		t.Fatalf("pending recovery lost: %#v, %v", got, err)
	}
	if err := c.Recover(); err != nil {
		t.Fatalf("clean recovery: %v", err)
	}
	if err := c.Apply(nil, &pending); !errors.Is(err, ErrReferenceConflict) {
		t.Fatalf("recovered source bypassed comparison: %v", err)
	}
}

func TestReferenceCoordinatorHonorsBothLocksAndTimeouts(t *testing.T) {
	for _, library := range []bool{true, false} {
		for _, timeout := range []time.Duration{-1, time.Millisecond} {
			c := newReferenceCoordinator(t)
			rec := versionRecord(t)
			release, err := image.LockLibrary(c.ImagesDir, 0)
			if err != nil {
				t.Fatal(err)
			}
			want := filelock.ErrHeld
			if !library {
				release()
				release, err = c.lockSession(rec.Name, 0)
				want = ErrLockHeld
			}
			if err != nil {
				t.Fatal(err)
			}
			c.Timeout = timeout
			err = c.Apply(nil, &rec)
			release()
			if timeout > 0 && library {
				want = filelock.ErrTimeout
			} else if timeout > 0 {
				want = ErrLockTimeout
			}
			if !errors.Is(err, want) {
				t.Fatalf("library=%t timeout=%v: %v, want %v", library, timeout, err, want)
			}
			if err := c.Apply(nil, &rec); err != nil {
				t.Fatalf("locks not released: %v", err)
			}
		}
	}
}

func TestReferenceCoordinatorRejectsUnrecoverableBookkeeping(t *testing.T) {
	for _, mode := range []string{"exhausted", "corrupt journal"} {
		c := newReferenceCoordinator(t)
		rec := versionRecord(t)
		state := ReferenceState{Version: ReferenceControlVersion}
		switch mode {
		case "exhausted":
			state.Generation = ^uint64(0)
		case "corrupt journal":
			path, _ := c.referencePath("journal.json")
			if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.SaveReferenceState(state); err != nil {
			t.Fatal(err)
		}
		if err := c.Apply(nil, &rec); err == nil {
			t.Fatalf("accepted %s bookkeeping", mode)
		}
		if _, err := c.LoadVersion(rec.Name); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s changed source: %v", mode, err)
		}
	}
	c := newReferenceCoordinator(t)
	c.ImagesDir = filepath.Join(t.TempDir(), "unused")
	if err := c.Apply(nil, nil); err == nil {
		t.Fatal("accepted empty request")
	}
	if _, err := os.Stat(c.ImagesDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid request touched image library: %v", err)
	}
}

func TestReferenceCoordinatorSerializesCompetingSnapshots(t *testing.T) {
	c := newReferenceCoordinator(t)
	c.Timeout = time.Second
	before := versionRecord(t)
	if err := c.Apply(nil, &before); err != nil {
		t.Fatal(err)
	}
	start, results := make(chan struct{}), make(chan error, 2)
	var workers sync.WaitGroup
	for _, builder := range []string{"one", "two"} {
		workers.Go(func() {
			after := before
			after.Actions = append([]Action(nil), before.Actions...)
			after.Actions[0].Mountpoint = filepath.Join(t.TempDir(), builder)
			<-start
			results <- c.Apply(&before, &after)
		})
	}
	close(start)
	workers.Wait()
	first, second := <-results, <-results
	if first != nil {
		first, second = second, first
	}
	if first != nil || !errors.Is(second, ErrReferenceConflict) {
		t.Fatalf("competing results = %v, %v", first, second)
	}
}
