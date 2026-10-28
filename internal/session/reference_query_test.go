//go:build linux || darwin

package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/filelock"
	"github.com/jiangxin/tiler/internal/image"
)

func TestTrustedReferencesRetainRolesWithoutReparsingSources(t *testing.T) {
	c := newReferenceCoordinator(t)
	rec := versionRecord(t)
	rec.State = StateClosed
	second := rec.Actions[0]
	second.Seq, second.RuntimeID, second.Status = 2, runtimeIDOf(2), StatusFailed
	rec.Actions = append(rec.Actions, second)
	if err := c.Apply(nil, &rec); err != nil {
		t.Fatal(err)
	}
	a := rec.Actions[0]
	targets := []ReferenceTarget{{a.Name, a.TipID}, {a.Name, a.ServedTip}, {"other/app", a.TipID}}
	before := mustReferenceState(t, c.Store)
	release, err := c.lockSession(rec.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	buckets, err := c.References(targets...)
	release()
	if err != nil || len(buckets) != 3 || len(buckets[0].Owners) != 2 ||
		len(buckets[1].Owners) != 2 || len(buckets[2].Owners) != 0 {
		t.Fatalf("trusted owners = %#v, %v", buckets, err)
	}
	if buckets[0].Owners[1].ActionID != 2 || buckets[1].Owners[0].Role != "served" ||
		mustReferenceState(t, c.Store).Generation != before.Generation {
		t.Fatal("roles, actions or unchanged-source fast path lost")
	}
	release, err = image.LockLibrary(c.ImagesDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, blocked := c.References(targets...)
	locked, err := c.ReferencesLocked(targets...)
	release()
	if !errors.Is(blocked, filelock.ErrHeld) || err != nil || len(locked[0].Owners) != 2 {
		t.Fatalf("caller-held lock lookup = %#v, %v, %v", locked, err, blocked)
	}
}

func TestTrustedReferencesRebuildMissingCorruptAndForgedShards(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt", "forged", "identity"} {
		t.Run(mode, func(t *testing.T) {
			c := newReferenceCoordinator(t)
			rec := versionRecord(t)
			if err := c.Apply(nil, &rec); err != nil {
				t.Fatal(err)
			}
			a := rec.Actions[0]
			path, _ := c.referenceBucketPath(a.Name, a.TipID)
			var err error
			switch mode {
			case "missing":
				err = os.Remove(path)
			case "corrupt":
				err = os.WriteFile(path, []byte("{"), 0o600)
			case "forged":
				err = c.SaveReferenceBucket(ReferenceBucket{Name: a.Name, Tip: a.TipID})
			case "identity":
				err = os.WriteFile(path, []byte(`{"name":"other/app"}`), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := mustReferenceState(t, c.Store).Generation
			got, err := c.References(ReferenceTarget{a.Name, a.TipID})
			if err != nil || len(got[0].Owners) != 1 || mustReferenceState(t, c.Store).Generation <= before {
				t.Fatalf("unsafe shard accepted: %#v, %v", got, err)
			}
		})
	}
}

func TestTrustedApplyRepairsShardsWithoutInheritingStaleOwners(t *testing.T) {
	for _, uncertain := range []string{"", "missing", "dirty", "corrupt"} {
		c := newReferenceCoordinator(t)
		before := versionRecord(t)
		if err := c.Apply(nil, &before); err != nil {
			t.Fatal(err)
		}
		a := before.Actions[0]
		path, _ := c.referenceBucketPath(a.Name, a.TipID)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if uncertain != "" {
			statePath, _ := c.referencePath("state.json")
			var err error
			switch uncertain {
			case "missing":
				err = os.Remove(statePath)
			case "dirty":
				state := mustReferenceState(t, c.Store)
				state.Dirty = true
				err = c.SaveReferenceState(state)
			case "corrupt":
				err = os.WriteFile(statePath, []byte("{"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		after := before
		after.Actions = append([]Action(nil), before.Actions...)
		after.Actions[0].TipID = strings.Repeat("e", 64)
		stale := ReferenceBucket{Name: a.Name, Tip: after.Actions[0].TipID,
			Owners: []ReferenceOwner{{"gone", strings.Repeat("f", 64), 99, "desired"}}}
		if err := c.SaveReferenceBucket(stale); err != nil {
			t.Fatal(err)
		}
		if err := c.Apply(&before, &after); err != nil {
			t.Fatal(err)
		}
		got, err := c.References(ReferenceTarget{a.Name, a.TipID}, ReferenceTarget{a.Name, stale.Tip})
		if err != nil || len(got[0].Owners) != 0 || len(got[1].Owners) != 1 || got[1].Owners[0].Session != before.Name {
			t.Fatalf("stale physical owners retained: %#v, %v", got, err)
		}
	}
}

func TestTrustedReferencesFollowManualSourcesAndFailClosed(t *testing.T) {
	c := newReferenceCoordinator(t)
	rec := versionRecord(t)
	if err := c.Apply(nil, &rec); err != nil {
		t.Fatal(err)
	}
	a := rec.Actions[0]
	rec.Actions[0].TipID = strings.Repeat("e", 64)
	if err := c.SaveVersion(rec); err != nil {
		t.Fatal(err)
	}
	got, err := c.References(ReferenceTarget{a.Name, a.TipID}, ReferenceTarget{a.Name, rec.Actions[0].TipID})
	if err != nil || len(got[0].Owners) != 0 || len(got[1].Owners) != 1 {
		t.Fatalf("manual source change ignored: %#v, %v", got, err)
	}
	path, _ := c.recordPath(rec.Name)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.References(ReferenceTarget{a.Name, a.TipID}); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("damaged source gave zero references: %v", err)
	}
	unused := c
	unused.ImagesDir = filepath.Join(t.TempDir(), "unused")
	if _, err := unused.References(ReferenceTarget{a.Name, "abcd"}); err == nil {
		t.Fatal("short query pin accepted")
	}
	if _, err := os.Stat(unused.ImagesDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid query touched library: %v", err)
	}
}

func TestPendingReferenceRecoveryReconstructsSharedOwners(t *testing.T) {
	for _, stage := range []string{"journal", "dirty", "source", "bucket", "inventory", "clean"} {
		for _, damage := range []string{"forged", "missing", "corrupt"} {
			t.Run(stage+"/"+damage, func(t *testing.T) {
				c, before, survivor, journal := sharedReferenceRemoval(t)
				if stage != "journal" {
					interrupted := errors.New("interrupted")
					err := c.withLibrary(func() error {
						return c.withSession(before.Name, func() error {
							return c.replayReferenceJournal(mustReferenceState(t, c.Store), journal, func(current string) error {
								if current == stage {
									return interrupted
								}
								return nil
							})
						})
					})
					if !errors.Is(err, interrupted) {
						t.Fatalf("checkpoint did not interrupt: %v", err)
					}
				}
				a := before.Actions[0]
				path, _ := c.referenceBucketPath(a.Name, a.TipID)
				var err error
				switch damage {
				case "forged":
					err = c.SaveReferenceBucket(ReferenceBucket{Name: a.Name, Tip: a.TipID})
				case "missing":
					err = os.Remove(path)
				case "corrupt":
					err = os.WriteFile(path, []byte("{"), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
				got, err := c.References(ReferenceTarget{a.Name, a.TipID})
				if err != nil || len(got) != 1 || len(got[0].Owners) != 1 || got[0].Owners[0].Session != survivor.Name {
					t.Fatalf("pending recovery lost surviving owner: %#v, %v", got, err)
				}
				persisted, err := c.LoadVersion(survivor.Name)
				if err != nil || !sameReferenceRecord(&persisted, &survivor) {
					t.Fatalf("surviving source changed: %#v, %v", persisted, err)
				}
				if _, err := c.LoadVersion(before.Name); !errors.Is(err, ErrNotFound) {
					t.Fatalf("removal was not recovered: %v", err)
				}
			})
		}
	}
}

func sharedReferenceRemoval(t *testing.T) (ReferenceCoordinator, Record, Record, ReferenceJournal) {
	t.Helper()
	c := newReferenceCoordinator(t)
	before, survivor := versionRecord(t), versionRecord(t)
	survivor.Name = "work"
	if err := errors.Join(c.Apply(nil, &before), c.Apply(nil, &survivor)); err != nil {
		t.Fatal(err)
	}
	state := mustReferenceState(t, c.Store)
	journal := ReferenceJournal{Version: ReferenceControlVersion, Generation: state.Generation + 1, Before: &before}
	if err := c.SaveReferenceJournal(journal); err != nil {
		t.Fatal(err)
	}
	return c, before, survivor, journal
}

func TestPendingReferenceReconstructionRejectsDamagedSourcesBeforeWrites(t *testing.T) {
	c, before, survivor, _ := sharedReferenceRemoval(t)
	a := before.Actions[0]
	if err := c.SaveReferenceBucket(ReferenceBucket{Name: a.Name, Tip: a.TipID}); err != nil {
		t.Fatal(err)
	}
	path, _ := c.recordPath(survivor.Name)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := mustReferenceState(t, c.Store)
	if _, err := c.References(ReferenceTarget{a.Name, a.TipID}); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("invalid reconstruction source accepted: %v", err)
	}
	unchanged, err := c.LoadVersion(before.Name)
	if err != nil || !sameReferenceRecord(&unchanged, &before) || mustReferenceState(t, c.Store).Generation != state.Generation {
		t.Fatalf("failed reconstruction changed source/state: %#v, %v", unchanged, err)
	}
	if _, err := c.LoadReferenceJournal(); err != nil {
		t.Fatalf("failed reconstruction discarded intent: %v", err)
	}
}
