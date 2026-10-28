//go:build linux || darwin

package session

import (
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/filelock"
	"github.com/jiangxin/tiler/internal/image"
)

func versionEngine(t *testing.T) (*Engine, ReferenceCoordinator, Action) {
	t.Helper()
	c := newReferenceCoordinator(t)
	e := &Engine{Store: c.Store, Versioned: true, ImagesDir: c.ImagesDir,
		Now: func() time.Time { return testNow }, LockTimeout: 2 * time.Second,
		Health: func(string) bool { return false }, Clean: func(string) error { return nil }}
	action := versionRecord(t).Actions[0]
	action.RuntimeID = ""
	return e, c, action
}

func requireVersionOwners(t *testing.T, c ReferenceCoordinator, action Action, desired, served int) {
	t.Helper()
	targets := []ReferenceTarget{{action.Name, action.TipID}, {action.Name, action.ServedTip}}
	buckets, err := c.References(targets...)
	if err != nil || len(buckets[0].Owners) != desired || len(buckets[1].Owners) != served {
		t.Fatalf("reference owners = %#v, %v; want %d/%d", buckets, err, desired, served)
	}
}

func TestVersionEngineCoreLifecycleMaintainsReferences(t *testing.T) {
	e, c, action := versionEngine(t)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	initial, err := c.LoadVersion("dev")
	if err != nil || initial.Version != VersionRecordVersion || initial.InstanceID == "" {
		t.Fatalf("new version record = %#v, %v", initial, err)
	}
	if err := e.New("dev"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate new = %v", err)
	}
	_, err = e.RunMount("dev", action, func(stored Action) (Result, error) {
		if release, err := image.LockLibrary(c.ImagesDir, 0); err == nil {
			release()
			t.Fatal("mount ran outside library lock")
		} else if !errors.Is(err, filelock.ErrHeld) {
			t.Fatal(err)
		}
		if release, err := c.lockSession("dev", 0); err == nil {
			release()
			t.Fatal("mount ran outside session lock")
		} else if !errors.Is(err, ErrLockHeld) {
			t.Fatal(err)
		}
		rec, err := c.LoadVersion("dev")
		if err != nil || rec.Actions[0].Status != StatusPending {
			t.Fatalf("write-ahead source = %#v, %v", rec, err)
		}
		buckets, err := c.ReferencesLocked(referenceTargets(&rec)...)
		if err != nil || len(buckets) != 2 || len(buckets[0].Owners) != 1 || len(buckets[1].Owners) != 1 {
			t.Fatalf("write-ahead references = %#v, %v", buckets, err)
		}
		return Result{Mountpoint: stored.Mountpoint}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, reused := range []bool{true, false} {
		if removed, err := e.Close("dev", func(string) error { return nil }); err != nil || removed {
			t.Fatalf("close = %v, %v", removed, err)
		}
		requireVersionOwners(t, c, action, 1, 1)
		if err := e.Open("dev", func(stored Action) (Result, error) {
			if stored.FullID != action.FullID || stored.TipID != action.TipID {
				t.Fatalf("reopen changed pinned identity: %#v", stored)
			}
			return Result{Mountpoint: stored.Mountpoint, Reused: reused}, nil
		}); err != nil {
			t.Fatal(err)
		}
		wantServed := 0
		if reused {
			wantServed = 1
		}
		requireVersionOwners(t, c, action, 1, wantServed)
	}
	final, err := c.LoadVersion("dev")
	if err != nil || final.InstanceID != initial.InstanceID || final.State != StateActive || final.Actions[0].Status != StatusMounted {
		t.Fatalf("final record = %#v, %v", final, err)
	}
	if e.scope != nil || e.Lock != nil {
		t.Fatal("core operation mutated the shared engine")
	}
}

func TestVersionEngineDirectWritesAndFailedMountRetainReferences(t *testing.T) {
	e, c, action := versionEngine(t)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	invalid := action
	invalid.TipID = "abcd"
	if _, err := e.RunMount("dev", invalid, func(Action) (Result, error) {
		t.Fatal("invalid pin reached mount")
		return Result{}, nil
	}); err == nil {
		t.Fatal("incomplete pin accepted")
	}
	failure := errors.New("mount failed")
	if _, err := e.RunMount("dev", action, func(Action) (Result, error) {
		return Result{}, failure
	}); !errors.Is(err, failure) {
		t.Fatalf("mount failure = %v", err)
	}
	requireVersionOwners(t, c, action, 1, 1)
	stored, err := e.Append("dev", action)
	if err != nil || stored.Seq != 2 || stored.Status != StatusPending {
		t.Fatalf("direct append = %#v, %v", stored, err)
	}
	if err := e.Writeback("dev", stored.Seq, StatusFailed, ""); err != nil {
		t.Fatal(err)
	}
	requireVersionOwners(t, c, action, 2, 2)
	if err := e.WritebackMounted("dev", stored.Seq, "", true); err != nil {
		t.Fatal(err)
	}
	requireVersionOwners(t, c, action, 2, 1)
	rec, err := c.LoadVersion("dev")
	if err != nil || len(rec.Actions) != 2 || rec.Actions[0].Status != StatusFailed {
		t.Fatalf("retained failed action = %#v, %v", rec, err)
	}
}

func TestVersionEngineFailedReopenPreservesPinsUntilRealRemount(t *testing.T) {
	e, c, action := versionEngine(t)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RunMount("dev", action, func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if removed, err := e.Close("dev", func(string) error { return nil }); err != nil || removed {
		t.Fatalf("close = %v, %v", removed, err)
	}
	want, err := c.LoadVersion("dev")
	if err != nil || want.State != StateClosed || want.ClosedAt.IsZero() {
		t.Fatalf("closed record = %#v, %v", want, err)
	}
	targets := []ReferenceTarget{{action.Name, action.TipID}, {action.Name, action.ServedTip}}
	owners, err := c.References(targets...)
	if err != nil {
		t.Fatal(err)
	}
	requireVersionOwners(t, c, action, 1, 1)
	failure := errors.New("replay mount failed")
	for _, stage := range []struct {
		name   string
		err    error
		reused bool
	}{
		{name: "failed replay", err: failure},
		{name: "adopted view", reused: true},
		{name: "real remount"},
	} {
		t.Run(stage.name, func(t *testing.T) {
			calls := 0
			err := e.Open("dev", func(stored Action) (Result, error) {
				calls++
				if !reflect.DeepEqual(stored, want.Actions[0]) {
					t.Fatalf("replay changed pinned action: %#v; want %#v", stored, want.Actions[0])
				}
				// Both roles remain published until a real remount succeeds.
				got, err := c.ReferencesLocked(targets...)
				if err != nil || !reflect.DeepEqual(got, owners) {
					t.Fatalf("references during replay = %#v, %v; want %#v", got, err, owners)
				}
				return Result{Mountpoint: stored.Mountpoint, Reused: stage.reused}, stage.err
			})
			if !errors.Is(err, stage.err) || calls != 1 {
				t.Fatalf("replay = %v, calls = %d; want %v, 1", err, calls, stage.err)
			}
			if stage.err != nil {
				want.Actions[0].Status = StatusFailed
			} else {
				want.State, want.ClosedAt = StateActive, time.Time{}
				want.Actions[0].Status = StatusMounted
				if !stage.reused {
					want.Actions[0].ServedID, want.Actions[0].ServedTip = "", ""
				}
			}
			got, err := c.LoadVersion("dev")
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("record after replay = %#v, %v; want %#v", got, err, want)
			}
			buckets, err := c.References(targets...)
			if err != nil || !reflect.DeepEqual(buckets[0], owners[0]) {
				t.Fatalf("desired references after replay = %#v, %v", buckets, err)
			}
			if stage.err != nil || stage.reused {
				if !reflect.DeepEqual(buckets[1], owners[1]) {
					t.Fatalf("served reference released before remount: %#v", buckets[1])
				}
			} else if len(buckets[1].Owners) != 0 {
				t.Fatalf("served reference retained after remount: %#v", buckets[1])
			}
		})
	}
}

func TestVersionEngineSerializesConcurrentCoreMounts(t *testing.T) {
	e, c, action := versionEngine(t)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			<-start
			if _, err := e.RunMount("dev", action, func(a Action) (Result, error) {
				return Result{Mountpoint: a.Mountpoint}, nil
			}); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	workers.Wait()
	requireVersionOwners(t, c, action, 2, 2)
	rec, err := c.LoadVersion("dev")
	if err != nil || len(rec.Actions) != 2 || rec.Actions[0].RuntimeID != "m1" || rec.Actions[1].RuntimeID != "m2" {
		t.Fatalf("concurrent record = %#v, %v", rec, err)
	}
}

func TestVersionEngineRemovableCloseReleasesItsIncarnation(t *testing.T) {
	e, c, action := versionEngine(t)
	if err := e.NewRemovable("dev"); err != nil {
		t.Fatal(err)
	}
	before, err := c.LoadVersion("dev")
	if err != nil || !before.RemoveOnClose {
		t.Fatalf("removable record = %#v, %v", before, err)
	}
	if _, err := e.RunMount("dev", action, func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if removed, err := e.Close("dev", func(string) error { return nil }); err != nil || !removed {
		t.Fatalf("removable close = %v, %v", removed, err)
	}
	requireVersionOwners(t, c, action, 0, 0)
	if _, err := c.LoadVersion("dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed record = %v", err)
	}
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	after, err := c.LoadVersion("dev")
	if err != nil || after.InstanceID == before.InstanceID {
		t.Fatalf("recreated record = %#v, %v", after, err)
	}
}

func TestVersionEngineRejectsLegacySourceBeforeCoreEffects(t *testing.T) {
	e, c, _ := versionEngine(t)
	if err := c.Save(Record{Name: "old", State: StateClosed}); err != nil {
		t.Fatal(err)
	}
	path, _ := c.recordPath("old")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Open("old", func(Action) (Result, error) {
		t.Fatal("legacy source reached replay")
		return Result{}, nil
	}); err == nil {
		t.Fatal("legacy source accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("legacy source was modified: %v", err)
	}
}
