package session

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func versionRecord(t *testing.T) Record {
	t.Helper()
	rec, err := NewVersionRecord("dev", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	action := sampleAction(1)
	action.FullID, action.TipID = strings.Repeat("a", 64), strings.Repeat("b", 64)
	action.ServedID, action.ServedTip = strings.Repeat("c", 64), strings.Repeat("d", 64)
	rec.Actions = []Action{action}
	return rec
}

func TestVersionRecordStoreAndRecreatedIdentity(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	rec := versionRecord(t)
	if err := store.SaveVersion(rec); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadVersion(rec.Name)
	if err != nil || !reflect.DeepEqual(got, rec) {
		t.Fatalf("round trip = %#v, %v", got, err)
	}
	other := versionRecord(t)
	if other.InstanceID == rec.InstanceID {
		t.Fatal("recreated session reused its identity")
	}
	path, _ := store.recordPath(rec.Name)
	before, _ := os.ReadFile(path)
	for name, edit := range map[string]func(*Record){
		"legacy":            func(r *Record) { r.Version = "" },
		"unknown schema":    func(r *Record) { r.Version = "9" },
		"instance":          func(r *Record) { r.InstanceID = "short" },
		"creation time":     func(r *Record) { r.CreatedAt = time.Time{} },
		"short tip":         func(r *Record) { r.Actions[0].TipID = "abcd" },
		"missing tip":       func(r *Record) { r.Actions[0].TipID = "" },
		"nonhex tip":        func(r *Record) { r.Actions[0].TipID = strings.Repeat("z", 64) },
		"uppercase tip":     func(r *Record) { r.Actions[0].TipID = strings.Repeat("A", 64) },
		"noncanonical name": func(r *Record) { r.Actions[0].Name = "alice//app" },
		"short root":        func(r *Record) { r.Actions[0].FullID = "abcd" },
		"unpaired served":   func(r *Record) { r.Actions[0].ServedTip = "" },
		"runtime identity":  func(r *Record) { r.Actions[0].RuntimeID = "m2" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := versionRecord(t)
			edit(&bad)
			if err := store.SaveVersion(bad); err == nil {
				t.Fatal("invalid version record was accepted")
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatal("rejected record changed stored bytes")
			}
		})
	}
	legacy := rec
	legacy.Version, legacy.InstanceID = "", ""
	if err := store.Save(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadVersion(rec.Name); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatal("strict reader accepted a legacy record")
	}
}

func TestVersionRecordExternalFileIdentity(t *testing.T) {
	rec := versionRecord(t)
	a := &rec.Actions[0]
	a.Name, a.FullID, a.ServedID, a.ServedTip = "", "", "", ""
	a.Image = filepath.Join(t.TempDir(), "external.sqsh")
	if err := rec.ValidateVersion(); err != nil {
		t.Fatal(err)
	}
	a.Image = "relative.sqsh"
	if err := rec.ValidateVersion(); err == nil {
		t.Fatal("relative file identity accepted")
	}
}
