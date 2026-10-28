package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func referenceReplayFixture(t *testing.T, mode string) (Store, ReferenceState, ReferenceJournal) {
	t.Helper()
	store := Store{Dir: t.TempDir()}
	before := versionRecord(t)
	after := before
	after.Actions = slices.Clone(before.Actions)
	after.Actions[0].TipID = strings.Repeat("e", 64)
	journal := ReferenceJournal{Version: ReferenceControlVersion, Generation: 11, Before: &before, After: &after}
	state := ReferenceState{Version: ReferenceControlVersion, Generation: 10}
	if mode == "create" {
		journal.Before = nil
	} else {
		if err := store.SaveVersion(before); err != nil {
			t.Fatal(err)
		}
		buckets, _ := store.ReferenceUpdates(ReferenceJournal{Version: ReferenceControlVersion, Generation: 1, After: &before})
		for _, bucket := range buckets {
			if err := store.SaveReferenceBucket(bucket); err != nil {
				t.Fatal(err)
			}
		}
		revision, err := store.referenceRevision(before)
		if err != nil {
			t.Fatal(err)
		}
		state.Inventory = map[string]ReferenceRevision{before.Name: revision}
	}
	if mode == "delete" {
		journal.After = nil
	}
	if err := errors.Join(store.SaveReferenceState(state), store.SaveReferenceJournal(journal)); err != nil {
		t.Fatal(err)
	}
	return store, state, journal
}

func TestReferenceReplaySurvivesEveryDurableTransition(t *testing.T) {
	interrupted := errors.New("interrupted")
	for _, mode := range []string{"create", "update", "delete"} {
		for stop := 1; stop <= 8; stop++ {
			t.Run(fmt.Sprintf("%s/%d", mode, stop), func(t *testing.T) {
				store, state, journal := referenceReplayFixture(t, mode)
				stages := 0
				err := store.replayReferenceJournal(state, journal, func(string) error {
					stages++
					if stages == stop {
						return interrupted
					}
					return nil
				})
				if (stages >= stop && !errors.Is(err, interrupted)) || (stages < stop && err != nil) {
					t.Fatalf("interruption = %v after %d stages", err, stages)
				}
				state, err = store.LoadReferenceState()
				if err != nil {
					t.Fatal(err)
				}
				if pending, err := store.LoadReferenceJournal(); err == nil {
					if err := store.replayReferenceJournal(state, pending, nil); err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				assertReferenceReplayComplete(t, store, journal)
				if err := store.replayReferenceJournal(mustReferenceState(t, store), journal, nil); err != nil {
					t.Fatalf("idempotent replay: %v", err)
				}
			})
		}
	}
}

func mustReferenceState(t *testing.T, store Store) ReferenceState {
	t.Helper()
	state, err := store.LoadReferenceState()
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func assertReferenceReplayComplete(t *testing.T, store Store, journal ReferenceJournal) {
	t.Helper()
	state := mustReferenceState(t, store)
	if state.Dirty || state.Generation != journal.Generation {
		t.Fatalf("unfinished state: %#v", state)
	}
	if _, err := store.LoadReferenceJournal(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal retained: %v", err)
	}
	rec, err := store.LoadVersion("dev")
	if journal.After == nil {
		if !errors.Is(err, ErrNotFound) || len(state.Inventory) != 0 {
			t.Fatalf("deleted source remains: %#v, %v", rec, err)
		}
	} else {
		revision, statErr := store.referenceRevision(rec)
		if err != nil || statErr != nil || !sameReferenceRecord(&rec, journal.After) || state.Inventory[rec.Name] != revision {
			t.Fatalf("source/inventory mismatch: %#v, %v, %v", rec, err, statErr)
		}
	}
	updates, err := store.ReferenceUpdates(journal)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range updates {
		got, err := store.LoadReferenceBucket(want.Name, want.Tip)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("incomplete reference bucket: %#v, %v", got, err)
		}
	}
}

func TestReferenceReplayRefusesConflictingSourcesAndGenerations(t *testing.T) {
	for _, mode := range []string{"source", "corrupt", "generation", "dirty", "completed", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			store, state, journal := referenceReplayFixture(t, "update")
			path, _ := store.recordPath("dev")
			switch mode {
			case "source":
				changed := *journal.Before
				changed.State = StateClosed
				if err := store.SaveVersion(changed); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "generation":
				state.Generation = 1
			case "dirty":
				state.Dirty = true
			case "completed":
				state.Generation = journal.Generation
			case "symlink":
				foreign := filepath.Join(t.TempDir(), "source")
				data, _ := os.ReadFile(path)
				if err := errors.Join(os.WriteFile(foreign, data, 0o600), os.Remove(path)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, path); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			if err := store.replayReferenceJournal(state, journal, nil); err == nil {
				t.Fatal("unsafe replay accepted")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) || mustReferenceState(t, store).Generation != 10 {
				t.Fatal("rejected replay changed source or state")
			}
		})
	}
}
