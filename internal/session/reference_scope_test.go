//go:build linux || darwin

package session

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jiangxin/tiler/internal/filelock"
	"github.com/jiangxin/tiler/internal/image"
)

func TestReferenceScopeReusesStoreMutationsUnderBothLocks(t *testing.T) {
	c := newReferenceCoordinator(t)
	rec := versionRecord(t)
	tip, served := rec.Actions[0].TipID, rec.Actions[0].ServedTip
	var retained Store
	err := c.WithSession(rec.Name, func(s Store) error {
		retained = s
		if release, err := image.LockLibrary(c.ImagesDir, 0); err == nil {
			release()
			t.Fatal("scope did not hold library lock")
		} else if !errors.Is(err, filelock.ErrHeld) {
			t.Fatal(err)
		}
		if release, err := c.lockSession(rec.Name, 0); err == nil {
			release()
			t.Fatal("scope did not hold session lock")
		} else if !errors.Is(err, ErrLockHeld) {
			t.Fatal(err)
		}
		if _, err := s.Load(rec.Name); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing source: %v", err)
		}
		if err := s.SaveVersion(rec); err != nil {
			return err
		}
		rec.Actions[0].TipID = strings.Repeat("e", 64)
		got, err := s.Load(rec.Name)
		if err != nil || got.Actions[0].TipID != tip {
			t.Fatalf("input alias changed scope: %#v, %v", got, err)
		}
		got.Actions[0].TipID = strings.Repeat("f", 64)
		got, err = s.Load(rec.Name)
		if err != nil || got.Actions[0].TipID != tip {
			t.Fatalf("output alias changed scope: %#v, %v", got, err)
		}
		action := got.Actions[0]
		action.ServedID, action.ServedTip = "", ""
		action.RuntimeID = ""
		appended, err := s.Append(rec.Name, action)
		if err != nil || appended.Seq != 2 || appended.Status != StatusPending {
			t.Fatalf("append = %#v, %v", appended, err)
		}
		if err := s.Writeback(rec.Name, appended.Seq, StatusFailed, ""); err != nil {
			return err
		}
		if err := s.WritebackMounted(rec.Name, 1, "", true); err != nil {
			return err
		}
		buckets, err := c.ReferencesLocked(ReferenceTarget{action.Name, tip}, ReferenceTarget{action.Name, served})
		if err != nil || len(buckets[0].Owners) != 2 || len(buckets[1].Owners) != 0 {
			t.Fatalf("per-save references = %#v, %v", buckets, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			if _, err := retained.Load(rec.Name); !errors.Is(err, ErrReferenceScopeClosed) {
				t.Errorf("retained load = %v", err)
			}
			if err := retained.SaveVersion(rec); !errors.Is(err, ErrReferenceScopeClosed) {
				t.Errorf("retained save = %v", err)
			}
		})
	}
	workers.Wait()
	if err := retained.deleteRecordFiles(rec.Name); !errors.Is(err, ErrReferenceScopeClosed) {
		t.Fatalf("retained removal = %v", err)
	}
}

func TestReferenceScopeRemovesAndRecreatesWithoutReacquiringLocks(t *testing.T) {
	c := newReferenceCoordinator(t)
	before := versionRecord(t)
	if err := c.Apply(nil, &before); err != nil {
		t.Fatal(err)
	}
	err := c.WithSession(before.Name, func(s Store) error {
		if err := s.deleteRecordFiles(before.Name); err != nil {
			return err
		}
		if _, err := s.Load(before.Name); !errors.Is(err, ErrNotFound) {
			t.Fatalf("removed source = %v", err)
		}
		after := versionRecord(t)
		after.Actions = nil
		return s.Save(after)
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := c.LoadVersion(before.Name)
	if err != nil || after.InstanceID == before.InstanceID {
		t.Fatalf("recreation = %#v, %v", after, err)
	}
	buckets, err := c.References(referenceTargets(&before)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, bucket := range buckets {
		if len(bucket.Owners) != 0 {
			t.Fatalf("removed incarnation retained owners: %#v", bucket)
		}
	}
}

func TestReferenceScopeReturnsSwallowedFailuresWithoutNewIntent(t *testing.T) {
	for _, mode := range []string{"source conflict", "new shard corruption"} {
		t.Run(mode, func(t *testing.T) {
			c := newReferenceCoordinator(t)
			before, other := versionRecord(t), versionRecord(t)
			other.Name, other.Actions[0].TipID = "other", strings.Repeat("e", 64)
			if err := errors.Join(c.Apply(nil, &before), c.Apply(nil, &other)); err != nil {
				t.Fatal(err)
			}
			var failure error
			err := c.WithSession(before.Name, func(s Store) error {
				after, err := s.Load(before.Name)
				if err != nil {
					return err
				}
				if mode == "source conflict" {
					changed := before
					changed.State = StateClosed
					if err := c.SaveVersion(changed); err != nil {
						return err
					}
				} else {
					after.Actions[0].TipID = other.Actions[0].TipID
					path, _ := c.referenceBucketPath(other.Actions[0].Name, other.Actions[0].TipID)
					if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
						return err
					}
				}
				failure = s.Save(after)
				if failure == nil || !errors.Is(s.Save(after), failure) {
					t.Fatalf("failed scope continued writing: %v", failure)
				}
				return nil
			})
			if failure == nil || !errors.Is(err, failure) || errors.Is(err, ErrLockHeld) {
				t.Fatalf("swallowed failure = %v, %v", failure, err)
			}
			if mode == "source conflict" && !errors.Is(err, ErrReferenceConflict) {
				t.Fatalf("source conflict = %v", err)
			}
			if _, err := c.LoadReferenceJournal(); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed preflight created intent: %v", err)
			}
		})
	}
}
