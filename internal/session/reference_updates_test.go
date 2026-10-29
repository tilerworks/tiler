package session

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestReferenceUpdatesPreserveOwnersAcrossReplayAndRecreation(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	before := versionRecord(t)
	before.Actions[0].Status = StatusFailed
	second := before.Actions[0]
	second.Seq, second.RuntimeID, second.Name = 2, "m2", "bob/app"
	second.ServedTip = second.TipID
	third := before.Actions[0]
	third.Seq, third.RuntimeID = 3, "m3"
	third.ServedID, third.ServedTip = "", ""
	external := second
	external.Seq, external.RuntimeID, external.Image = 4, "m4", "/external.sqsh"
	external.Name, external.FullID, external.ServedID, external.ServedTip = "", "", "", ""
	before.Actions = append(before.Actions, second, third, external)
	journal := ReferenceJournal{Version: ReferenceControlVersion, Generation: 1, After: &before}
	updates, err := store.ReferenceUpdates(journal)
	if err != nil || len(updates) != 3 {
		t.Fatalf("create plan = %#v, %v", updates, err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir, referenceDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planning wrote reference data: %v", err)
	}
	persist := func(updates []ReferenceBucket) {
		t.Helper()
		for _, bucket := range updates {
			if err := store.SaveReferenceBucket(bucket); err != nil {
				t.Fatal(err)
			}
		}
	}
	persist(updates)
	foreign := ReferenceOwner{Session: "work", InstanceID: strings.Repeat("e", 64), ActionID: 1, Role: "desired"}
	bucket, err := store.LoadReferenceBucket("alice/app", before.Actions[0].TipID)
	if err != nil {
		t.Fatal(err)
	}
	bucket.Owners = append(bucket.Owners, foreign)
	if err := store.SaveReferenceBucket(bucket); err != nil {
		t.Fatal(err)
	}
	after := versionRecord(t)
	after.Actions = append([]Action(nil), before.Actions...)
	after.Actions[0].TipID = after.Actions[0].ServedTip
	after.State = StateClosed
	journal.Before, journal.After = &before, &after
	updates, err = store.ReferenceUpdates(journal)
	if err != nil || len(updates) != 3 {
		t.Fatalf("replacement plan = %#v, %v", updates, err)
	}
	for _, bucket := range updates {
		want := 2
		if bucket.Name == "alice/app" && bucket.Tip == before.Actions[0].TipID {
			if !slices.Contains(bucket.Owners, foreign) {
				t.Fatalf("unrelated owner lost: %#v", bucket)
			}
		}
		if len(bucket.Owners) != want {
			t.Fatalf("mount roles lost: %#v", bucket)
		}
		for _, owner := range bucket.Owners {
			if owner.Session == before.Name && owner.InstanceID != after.InstanceID {
				t.Fatalf("old incarnation retained: %#v", owner)
			}
		}
	}
	persist(updates)
	again, err := store.ReferenceUpdates(journal)
	if err != nil || !reflect.DeepEqual(updates, again) {
		t.Fatalf("replay changed replacements: %#v, %v", again, err)
	}
	journal.Before, journal.After = &after, nil
	updates, err = store.ReferenceUpdates(journal)
	if err != nil || len(updates) != 3 {
		t.Fatalf("delete plan = %#v, %v", updates, err)
	}
	for _, bucket := range updates {
		want := 0
		if bucket.Name == "alice/app" && bucket.Tip == before.Actions[0].TipID {
			want = 1
		}
		if len(bucket.Owners) != want {
			t.Fatalf("removed session still referenced: %#v", bucket)
		}
	}
	persist(updates)
	again, err = store.ReferenceUpdates(journal)
	if err != nil || !reflect.DeepEqual(updates, again) {
		t.Fatalf("empty-shard replay changed: %#v, %v", again, err)
	}
}

func TestReferenceUpdatesRejectMissingOldAndCorruptShards(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	rec := versionRecord(t)
	journal := ReferenceJournal{Version: ReferenceControlVersion, Generation: 1, Before: &rec, After: &rec}
	if updates, err := store.ReferenceUpdates(journal); !errors.Is(err, os.ErrNotExist) || updates != nil {
		t.Fatalf("missing old shard treated as empty: %#v, %v", updates, err)
	}
	journal.Before = nil
	updates, err := store.ReferenceUpdates(journal)
	if err != nil {
		t.Fatal(err)
	}
	for _, bucket := range updates {
		if err := store.SaveReferenceBucket(bucket); err != nil {
			t.Fatal(err)
		}
	}
	path, _ := store.referenceBucketPath(rec.Actions[0].Name, rec.Actions[0].TipID)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if updates, err := store.ReferenceUpdates(journal); err == nil || errors.Is(err, os.ErrNotExist) || updates != nil {
		t.Fatalf("corrupt shard treated as empty: %#v, %v", updates, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "{" {
		t.Fatal("failed planning changed shard")
	}
	journal.Version = 2
	if _, err := store.ReferenceUpdates(journal); err == nil {
		t.Fatal("invalid journal accepted")
	}
}
