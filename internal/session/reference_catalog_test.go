package session

import (
	"errors"
	"maps"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestReferenceCatalogValidatesIdentitiesAndDigests(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	key := referenceCatalogKey("alice/app", strings.Repeat("a", 64))
	state := ReferenceState{Version: ReferenceControlVersion, CatalogComplete: true,
		ShardDigests: map[string]string{key: strings.Repeat("b", 64)}}
	if err := store.SaveReferenceState(state); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadReferenceState()
	if err != nil || !reflect.DeepEqual(got, state) {
		t.Fatalf("catalog roundtrip = %#v, %v", got, err)
	}
	for _, invalid := range []map[string]string{
		{"alice/app": state.ShardDigests[key]}, {"alice/app:abcd": state.ShardDigests[key]},
		{"alice/../app:" + strings.Repeat("a", 64): state.ShardDigests[key]},
		{key: "short"}, {key: strings.Repeat("B", 64)},
	} {
		state.ShardDigests = invalid
		if err := store.SaveReferenceState(state); err == nil {
			t.Fatalf("invalid catalog accepted: %#v", invalid)
		}
	}
	after, err := store.LoadReferenceState()
	if err != nil || !reflect.DeepEqual(after, got) {
		t.Fatalf("rejected catalog changed state: %#v, %v", after, err)
	}
}

func TestReferenceReplayPreservesCatalogCompletenessAndIsolation(t *testing.T) {
	for _, complete := range []bool{false, true} {
		store, state, journal := referenceReplayFixture(t, "update")
		foreign := ReferenceBucket{Name: "bob/app", Tip: strings.Repeat("f", 64)}
		if err := store.SaveReferenceBucket(foreign); err != nil {
			t.Fatal(err)
		}
		key := referenceCatalogKey(foreign.Name, foreign.Tip)
		digest, err := store.referenceShardDigest(foreign.Name, foreign.Tip)
		if err != nil {
			t.Fatal(err)
		}
		state.CatalogComplete, state.ShardDigests[key] = complete, digest
		original := maps.Clone(state.ShardDigests)
		if err := errors.Join(store.SaveReferenceState(state), store.replayReferenceJournal(state, journal, nil)); err != nil {
			t.Fatal(err)
		}
		got := mustReferenceState(t, store)
		if got.CatalogComplete != complete || got.ShardDigests[key] != digest || !maps.Equal(state.ShardDigests, original) {
			t.Fatalf("partial replay changed trust or unrelated catalog: %#v", got)
		}
		assertReferenceReplayComplete(t, store, journal)
	}
}

func TestReferenceCatalogRetainsEvidenceForMissingAndChangedShards(t *testing.T) {
	store, state, journal := referenceReplayFixture(t, "create")
	if err := store.replayReferenceJournal(state, journal, nil); err != nil {
		t.Fatal(err)
	}
	state = mustReferenceState(t, store)
	action := journal.After.Actions[0]
	key := referenceCatalogKey(action.Name, action.TipID)
	want := state.ShardDigests[key]
	path, _ := store.referenceBucketPath(action.Name, action.TipID)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.referenceShardDigest(action.Name, action.TipID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost shard = %v", err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if digest, err := store.referenceShardDigest(action.Name, action.TipID); err != nil || digest == want {
		t.Fatalf("changed shard not detectable: %s, %v", digest, err)
	}
	if got := mustReferenceState(t, store); !got.CatalogComplete || got.ShardDigests[key] != want {
		t.Fatal("shard loss/corruption erased expected catalog evidence")
	}
}
