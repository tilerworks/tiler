package session

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReferenceControlsPreserveStateAndTransactionSnapshots(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	rec := versionRecord(t)
	state := ReferenceState{Version: ReferenceControlVersion, Generation: 3, Dirty: true,
		Inventory: map[string]ReferenceRevision{rec.Name: {InstanceID: rec.InstanceID, SHA256: rec.Actions[0].TipID, Size: 12, ModifiedNano: 1}}}
	journal := ReferenceJournal{Version: ReferenceControlVersion, Generation: 3, Before: &rec, After: &rec}
	if _, err := store.LoadReferenceJournal(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing journal = %v", err)
	}
	for _, want := range []ReferenceState{{Version: ReferenceControlVersion}, state} {
		if err := store.SaveReferenceState(want); err != nil {
			t.Fatal(err)
		}
		got, err := store.LoadReferenceState()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("state = %#v, %v", got, err)
		}
	}
	for _, want := range []ReferenceJournal{journal,
		{Version: ReferenceControlVersion, Generation: 1, Before: &rec},
		{Version: ReferenceControlVersion, Generation: 2, After: &rec}} {
		if err := store.SaveReferenceJournal(want); err != nil {
			t.Fatal(err)
		}
		txn, err := store.LoadReferenceJournal()
		if err != nil || !reflect.DeepEqual(txn, want) {
			t.Fatalf("journal = %#v, %v", txn, err)
		}
	}
	path, _ := store.referencePath("state.json")
	before, _ := os.ReadFile(path)
	other := rec
	other.Name = "other"
	for _, invalid := range []referenceControl{ReferenceState{}, ReferenceState{Version: ReferenceControlVersion, Dirty: true},
		ReferenceState{Version: ReferenceControlVersion, Inventory: map[string]ReferenceRevision{rec.Name: {}}},
		ReferenceJournal{}, ReferenceJournal{Version: ReferenceControlVersion, Generation: 1},
		ReferenceJournal{Version: ReferenceControlVersion, Generation: 1, Before: &Record{}},
		ReferenceJournal{Version: ReferenceControlVersion, Before: &rec},
		ReferenceJournal{Version: ReferenceControlVersion, Generation: 1, Before: &rec, After: &other},
	} {
		if err := store.saveReferenceControl("state.json", invalid); err == nil {
			t.Fatalf("accepted invalid control %#v", invalid)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid write changed prior state")
	}
	unsafe := Store{Dir: t.TempDir()}
	if err := os.Symlink(filepath.Join(store.Dir, referenceDir), filepath.Join(unsafe.Dir, referenceDir)); err != nil {
		t.Fatal(err)
	}
	if err := unsafe.SaveReferenceState(state); err == nil {
		t.Fatal("accepted unsafe control path")
	}
	for _, body := range []string{"{", `{ "version": 9 }`, `{ "version": "1.0", "unknown": true }`, `{} {}`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadReferenceState(); err == nil {
			t.Fatal("accepted corrupt state")
		}
	}
}
