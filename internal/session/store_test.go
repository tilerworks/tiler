package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRecord writes one empty active record and returns the store.
func newRecord(t *testing.T, dir, name string) Store {
	t.Helper()
	store := Store{Dir: dir}
	rec := Record{
		Name:      name,
		State:     StateActive,
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	}
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSaveLoadRoundTripCreatesPrivateFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	store := newRecord(t, dir, "dev")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("sessions dir mode = %v, want 0700", info.Mode().Perm())
	}
	info, err = os.Stat(filepath.Join(dir, "dev"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("session dir mode = %v, want 0700", info.Mode().Perm())
	}
	info, err = os.Stat(filepath.Join(dir, "dev", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("record mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := store.Load("dev"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingRecordReportsNotFound(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	_, err := store.Load("absent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load error = %v, want ErrNotFound", err)
	}
}

func TestLoadRejectsInvalidName(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if _, err := store.Load("a/b"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Load error = %v, want ErrInvalidName", err)
	}
}

func TestLoadReportsCorruptRecord(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	if err := os.MkdirAll(filepath.Join(dir, "bad"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad", "session.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("bad"); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("Load error = %v, want a parse failure", err)
	}
}

func TestLoadRejectsForeignRecordName(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	if err := os.MkdirAll(filepath.Join(dir, "dev"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"other","state":"active","actions":null,"created_at":"2026-10-16T09:00:00Z","closed_at":"0001-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "dev", "session.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("dev"); err == nil {
		t.Fatal("a record naming another session loaded")
	}
}

func TestSaveOverwriteLeavesSingleFile(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "dev")
	rec, err := store.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	rec.State = StateClosed
	rec.ClosedAt = time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "dev" || !entries[0].IsDir() {
		t.Fatalf("directory entries = %v, want only dev/", entries)
	}
	inner, err := os.ReadDir(filepath.Join(dir, "dev"))
	if err != nil {
		t.Fatal(err)
	}
	if len(inner) != 1 || inner[0].Name() != "session.json" {
		t.Fatalf("session dir entries = %v, want only session.json", inner)
	}
	loaded, err := store.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateClosed || loaded.ClosedAt.IsZero() {
		t.Fatalf("overwrite lost: %+v", loaded)
	}
}

func TestSaveFailureLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "dev")
	// A record path that is a non-empty directory makes the final
	// rename fail, the same way a half-occupied target would.
	recPath := filepath.Join(dir, "dev", "session.json")
	if err := os.Remove(recPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(recPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recPath, "inside"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := store.Load("dev")
	if err == nil {
		t.Fatalf("the blocked target loaded: %+v", rec)
	}
	rec = Record{Name: "dev", State: StateActive}
	if err := store.Save(rec); err == nil {
		t.Fatal("Save over a directory succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "dev" {
		t.Fatalf("failed write left residue: %v", entries)
	}
}

func TestSaveRejectsInvalidRecord(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	if err := store.Save(Record{Name: "dev", State: "paused"}); err == nil {
		t.Fatal("Save accepted an unknown state")
	}
}

func TestAppendIsDurableBeforeExecution(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "dev")
	action := Action{
		Type:       TypeMount,
		Image:      "alice/app",
		Mountpoint: filepath.Join(dir, "view"),
		Mode:       ModeRW,
	}
	// The fake executor reads the record back from disk while it
	// runs: the write-ahead contract means the pending action is
	// already fsynced before any mount executes.
	executor := func(stored Action) error {
		fresh := Store{Dir: dir}
		rec, err := fresh.Load("dev")
		if err != nil {
			return err
		}
		if len(rec.Actions) != 1 {
			return fmt.Errorf("disk holds %d actions, want 1", len(rec.Actions))
		}
		onDisk := rec.Actions[0]
		if onDisk.Status != StatusPending {
			return fmt.Errorf("disk status = %q, want pending", onDisk.Status)
		}
		if onDisk.Seq != stored.Seq || onDisk.Image != stored.Image {
			return fmt.Errorf("disk action = %+v, stored = %+v", onDisk, stored)
		}
		return nil
	}
	stored, err := store.Append("dev", action)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Seq != 1 || stored.Status != StatusPending {
		t.Fatalf("stored = %+v", stored)
	}
	if stored.Timestamp.IsZero() {
		t.Error("Append left the timestamp zero")
	}
	if err := executor(stored); err != nil {
		t.Fatal(err)
	}
}

func TestAppendAssignsIncreasingSeq(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "dev")
	for want := 1; want <= 3; want++ {
		stored, err := store.Append("dev", Action{
			Type: TypeMount, Image: "alice/app", Mode: ModeRO,
		})
		if err != nil {
			t.Fatal(err)
		}
		if stored.Seq != want {
			t.Fatalf("seq = %d, want %d", stored.Seq, want)
		}
		// An explicit status never survives the write-ahead append.
		if stored.Status != StatusPending {
			t.Fatalf("status = %q, want pending", stored.Status)
		}
	}
	rec, err := store.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Actions) != 3 {
		t.Fatalf("actions = %+v", rec.Actions)
	}
}

func TestAppendKeepsExplicitTimestamp(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "dev")
	stamp := time.Date(2026, 10, 17, 3, 15, 0, 0, time.UTC)
	stored, err := store.Append("dev", Action{
		Type: TypeMount, Image: "alice/app", Mode: ModeRO, Timestamp: stamp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Timestamp.Equal(stamp) {
		t.Fatalf("timestamp = %v, want %v", stored.Timestamp, stamp)
	}
}

func TestAppendMissingRecordFails(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	_, err := store.Append("absent", Action{Type: TypeMount})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Append error = %v, want ErrNotFound", err)
	}
}

func TestWritebackUpdatesStatusAndMountpoint(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "dev")
	stored, err := store.Append("dev", Action{
		Type: TypeMount, Image: "alice/app", Mountpoint: "/planned", Mode: ModeRW,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Writeback("dev", stored.Seq, StatusMounted, "/actual"); err != nil {
		t.Fatal(err)
	}
	rec, err := store.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Actions[0].Status != StatusMounted || rec.Actions[0].Mountpoint != "/actual" {
		t.Fatalf("action = %+v", rec.Actions[0])
	}
	// An empty mountpoint keeps the recorded one.
	if err := store.Writeback("dev", stored.Seq, StatusFailed, ""); err != nil {
		t.Fatal(err)
	}
	rec, err = store.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Actions[0].Status != StatusFailed || rec.Actions[0].Mountpoint != "/actual" {
		t.Fatalf("action = %+v", rec.Actions[0])
	}
}

func TestWritebackRejectsUnknownSeqAndStatus(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "dev")
	if _, err := store.Append("dev", Action{Type: TypeMount, Mode: ModeRW}); err != nil {
		t.Fatal(err)
	}
	if err := store.Writeback("dev", 7, StatusMounted, ""); err == nil {
		t.Fatal("Writeback accepted an unknown seq")
	}
	if err := store.Writeback("dev", 1, "done", ""); err == nil {
		t.Fatal("Writeback accepted an unknown status")
	}
	if _, err := store.Load("dev"); err != nil {
		t.Fatalf("rejected writebacks damaged the record: %v", err)
	}
}

func TestWritebackMountedFoldsServedClearIntoOneWrite(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "dev")
	seed := Action{
		Type: TypeMount, Image: "alice/app", Mountpoint: "/planned",
		Mode: ModeRW, TipID: fixtureTip,
		ServedID: fixtureServedID, ServedTip: fixtureServedTip,
	}
	adopted, err := store.Append("dev", seed)
	if err != nil {
		t.Fatal(err)
	}
	remounted, err := store.Append("dev", seed)
	if err != nil {
		t.Fatal(err)
	}
	// An adopted view keeps the served pair: it still serves the
	// stack those ids name.
	if err := store.WritebackMounted("dev", adopted.Seq, "/actual", false); err != nil {
		t.Fatal(err)
	}
	// A real remount lands the mounted status, the resolved
	// mountpoint, and the emptied served pair through one atomic
	// write, so no intermediate record ever pairs status=mounted
	// with stale served ids for a crash to strand.
	if err := store.WritebackMounted("dev", remounted.Seq, "", true); err != nil {
		t.Fatal(err)
	}
	rec, err := store.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	kept := rec.Actions[0]
	if kept.Status != StatusMounted || kept.Mountpoint != "/actual" {
		t.Fatalf("adopted action = %+v", kept)
	}
	if kept.ServedID != fixtureServedID || kept.ServedTip != fixtureServedTip {
		t.Errorf("an adopted view lost its served stack: %+v", kept)
	}
	cleared := rec.Actions[1]
	if cleared.Status != StatusMounted || cleared.Mountpoint != "/planned" {
		t.Fatalf("remounted action = %+v", cleared)
	}
	if cleared.ServedID != "" || cleared.ServedTip != "" {
		t.Errorf("a real remount kept the served stack: %+v", cleared)
	}
	if cleared.TipID != fixtureTip {
		t.Errorf("the writeback changed the desired tip: %+v", cleared)
	}
	// Like Writeback, an unknown seq is an error, never a silent
	// pass over the unmodified record.
	if err := store.WritebackMounted("dev", 7, "", true); err == nil {
		t.Fatal("WritebackMounted accepted an unknown seq")
	}
}

func TestListOrdersRecordsAndSkipsForeignFiles(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "zeta")
	newRecord(t, dir, "alpha")
	newRecord(t, dir, "mid")
	for _, foreign := range []string{"notes.txt", "dev.json.bak", "mid.lock"} {
		if err := os.WriteFile(filepath.Join(dir, foreign), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A stray directory holding no record is not a session.
	if err := os.Mkdir(filepath.Join(dir, "stray"), 0o700); err != nil {
		t.Fatal(err)
	}
	records, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, rec := range records {
		names = append(names, rec.Name)
	}
	if strings.Join(names, ",") != "alpha,mid,zeta" {
		t.Fatalf("names = %v", names)
	}
}

func TestListReportsCorruptRecords(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "good")
	if err := os.MkdirAll(filepath.Join(dir, "broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken", "session.json"), []byte(":::"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.List()
	if err == nil {
		t.Fatal("List dropped a corrupt record silently")
	}
	if !strings.Contains(err.Error(), "session.json") {
		t.Fatalf("List error = %v, want the file name", err)
	}
	if err := os.Remove(filepath.Join(dir, "broken", "session.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err != nil {
		t.Fatal(err)
	}
}

func TestListMissingDirIsEmpty(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), "absent")}
	records, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("records = %+v", records)
	}
}

func TestListSkipsInvalidSessionDirName(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "dev")
	// A stray directory whose name is no legal session name must be
	// skipped, matching the scan path, so it cannot break a listing.
	if err := os.Mkdir(filepath.Join(dir, "bad name"), 0o700); err != nil {
		t.Fatal(err)
	}
	records, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Name != "dev" {
		t.Fatalf("records = %+v, want only dev", records)
	}
}

func TestAppendAssignsRuntimeIDForMountsOnly(t *testing.T) {
	dir := t.TempDir()
	store := newRecord(t, dir, "dev")
	first, err := store.Append("dev", Action{
		Type: TypeMount, Image: "alice/app", Mode: ModeRW,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.RuntimeID != "m1" {
		t.Fatalf("first runtime id = %q, want m1", first.RuntimeID)
	}
	second, err := store.Append("dev", Action{
		Type: TypeMount, Image: "alice/app", Mode: ModeRO,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.RuntimeID != "m2" {
		t.Fatalf("second runtime id = %q, want m2", second.RuntimeID)
	}
	// A umount refers to an earlier mount and carries no runtime id.
	umount, err := store.Append("dev", Action{Type: TypeUmount})
	if err != nil {
		t.Fatal(err)
	}
	if umount.RuntimeID != "" {
		t.Fatalf("umount runtime id = %q, want empty", umount.RuntimeID)
	}
	rec, err := store.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Actions[0].RuntimeID != "m1" || rec.Actions[1].RuntimeID != "m2" ||
		rec.Actions[2].RuntimeID != "" {
		t.Fatalf("runtime ids did not persist: %+v", rec.Actions)
	}
}
