package session

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReferenceBucketsPreserveMountOwnersAndLibraryScope(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	rec := versionRecord(t)
	owner := ReferenceOwner{Session: rec.Name, InstanceID: rec.InstanceID, ActionID: 2, Role: "desired"}
	bucket := ReferenceBucket{Name: "alice/app", Tip: strings.Repeat("a", 64), Owners: []ReferenceOwner{owner}}
	owner.ActionID, owner.Role = 1, "served"
	bucket.Owners = append(bucket.Owners, owner)
	for _, name := range []string{"alice/app", "bob/app"} {
		bucket.Name = name
		if err := store.SaveReferenceBucket(bucket); err != nil {
			t.Fatal(err)
		}
		got, err := store.LoadReferenceBucket(name, bucket.Tip)
		if err != nil || len(got.Owners) != 2 || got.Owners[0] != owner || got.Name != name {
			t.Fatalf("bucket = %#v, %v", got, err)
		}
	}
	bucket.Owners = nil
	if err := store.SaveReferenceBucket(bucket); err != nil {
		t.Fatal(err)
	}
	other, err := store.LoadReferenceBucket("alice/app", bucket.Tip)
	if err != nil || len(other.Owners) != 2 || bucket.Owners != nil {
		t.Fatalf("cross-library mutation: %#v, %v", other, err)
	}
	if _, err := store.LoadReferenceBucket("missing", bucket.Tip); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing bucket = %v", err)
	}
	if err := store.SaveVersion(rec); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List()
	names, scanErr := store.names()
	if err != nil || scanErr != nil || len(listed) != 1 || !reflect.DeepEqual(names, []string{rec.Name}) || ValidateName(referenceDir) == nil {
		t.Fatalf("reserved directory listed: %#v, %v, %v, %v", names, listed, err, scanErr)
	}
	unsafe := Store{Dir: t.TempDir()}
	if err := os.Symlink(filepath.Join(store.Dir, referenceDir), filepath.Join(unsafe.Dir, referenceDir)); err != nil {
		t.Fatal(err)
	}
	if err := unsafe.SaveReferenceBucket(ReferenceBucket{Name: "alice/app", Tip: bucket.Tip}); err == nil {
		t.Fatal("symlink allowed a foreign bucket overwrite")
	}
	if _, err := unsafe.LoadReferenceBucket("alice/app", bucket.Tip); err == nil || ValidateName(".REFS") == nil {
		t.Fatal("unsafe index path or case alias accepted")
	}
	after, err := store.LoadReferenceBucket("alice/app", bucket.Tip)
	if err != nil || !reflect.DeepEqual(after, other) {
		t.Fatalf("rejected write changed foreign shard: %#v, %v", after, err)
	}
}

func TestReferenceBucketRejectsInvalidOrUnsafeData(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	owner := ReferenceOwner{Session: "dev", InstanceID: strings.Repeat("a", 64), ActionID: 1, Role: "desired"}
	bucket := ReferenceBucket{Name: "alice/app", Tip: strings.Repeat("b", 64), Owners: []ReferenceOwner{owner}}
	for _, change := range []func(*ReferenceBucket){
		func(b *ReferenceBucket) { b.Name = "alice/../app" },
		func(b *ReferenceBucket) { b.Tip = "abcd" },
		func(b *ReferenceBucket) { b.Owners[0].Session = referenceDir },
		func(b *ReferenceBucket) { b.Owners[0].InstanceID = "short" },
		func(b *ReferenceBucket) { b.Owners[0].ActionID = 0 },
		func(b *ReferenceBucket) { b.Owners[0].Role = "unknown" },
		func(b *ReferenceBucket) { b.Owners = append(b.Owners, b.Owners[0]) },
	} {
		bad := bucket
		bad.Owners = []ReferenceOwner{owner}
		change(&bad)
		if err := store.SaveReferenceBucket(bad); err == nil {
			t.Fatalf("invalid bucket accepted: %#v", bad)
		}
	}
	if err := store.SaveReferenceBucket(bucket); err != nil {
		t.Fatal(err)
	}
	path, _ := store.referenceBucketPath(bucket.Name, bucket.Tip)
	for _, body := range []string{"{", `{"name":"bob/app"}`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.LoadReferenceBucket(bucket.Name, bucket.Tip); err == nil {
			t.Fatal("corrupt or mismatched bucket accepted")
		}
	}
}
