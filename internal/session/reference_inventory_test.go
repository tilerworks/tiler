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

func TestReferenceInventoryRebuildsAndProbesSources(t *testing.T) {
	c := newReferenceCoordinator(t)
	rec := versionRecord(t)
	if err := c.SaveVersion(rec); err != nil {
		t.Fatal(err)
	}
	state, err := c.EnsureInventory()
	if err != nil || !state.CatalogComplete || state.Dirty || len(state.Inventory) != 1 {
		t.Fatalf("initial rebuild = %#v, %v", state, err)
	}
	// Unchanged inventory needs no session lock or Action decoding/rebuild.
	release, err := c.lockSession(rec.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := c.EnsureInventory()
	release()
	if err != nil || unchanged.Generation != state.Generation {
		t.Fatalf("unchanged probe rebuilt source: %#v, %v", unchanged, err)
	}
	rec.State, rec.Actions[0].TipID = StateClosed, strings.Repeat("e", 64)
	if err := c.SaveVersion(rec); err != nil {
		t.Fatal(err)
	}
	added := versionRecord(t)
	added.Name = "added"
	if err := c.SaveVersion(added); err != nil {
		t.Fatal(err)
	}
	state, err = c.EnsureInventory()
	if err != nil || len(state.Inventory) != 2 || len(state.ShardDigests) != 3 {
		t.Fatalf("edited/added sources lost: %#v, %v", state, err)
	}
	path, _ := c.recordPath(rec.Name)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	state, err = c.EnsureInventory()
	if err != nil || len(state.Inventory) != 1 || len(state.ShardDigests) != 2 {
		t.Fatalf("deleted source retained: %#v, %v", state, err)
	}
	for key, digest := range state.ShardDigests {
		name, tip, _ := strings.Cut(key, ":")
		got, err := c.referenceShardDigest(name, tip)
		if err != nil || got != digest {
			t.Fatalf("rebuilt shard mismatch: %s, %v", key, err)
		}
	}
}

func TestReferenceInventoryRecoversJournalBeforeRebuild(t *testing.T) {
	c := newReferenceCoordinator(t)
	rec := versionRecord(t)
	journal := ReferenceJournal{Version: ReferenceControlVersion, Generation: 1, After: &rec}
	if err := c.SaveReferenceJournal(journal); err != nil {
		t.Fatal(err)
	}
	state, err := c.EnsureInventory()
	got, sourceErr := c.LoadVersion(rec.Name)
	if err != nil || sourceErr != nil || !sameReferenceRecord(&got, &rec) || !state.CatalogComplete {
		t.Fatalf("pending journal not recovered: %#v, %v, %v", state, err, sourceErr)
	}
	if _, err := c.LoadReferenceJournal(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal retained: %v", err)
	}
}

func TestReferenceInventoryRebuildResumesEveryStage(t *testing.T) {
	for stop := 1; stop <= 4; stop++ {
		c := newReferenceCoordinator(t)
		rec := versionRecord(t)
		if err := c.SaveVersion(rec); err != nil {
			t.Fatal(err)
		}
		stages := 0
		interrupted := errors.New("interrupted")
		err := c.withLibrary(func() error {
			_, err := c.ensureInventoryLocked(true, func(string) error {
				stages++
				if stages == stop {
					return interrupted
				}
				return nil
			})
			return err
		})
		if !errors.Is(err, interrupted) {
			t.Fatalf("stage %d: %v", stop, err)
		}
		state, err := c.EnsureInventory()
		if err != nil || state.Dirty || !state.CatalogComplete || len(state.ShardDigests) != 2 {
			t.Fatalf("stage %d recovery = %#v, %v", stop, state, err)
		}
	}
}

func TestReferenceInventoryRejectsDamagedSourcesBeforeIndexWrites(t *testing.T) {
	for _, mode := range []string{"corrupt", "legacy", "symlink", "unreadable", "held", "journal", "library"} {
		t.Run(mode, func(t *testing.T) {
			c := newReferenceCoordinator(t)
			rec := versionRecord(t)
			if err := c.SaveVersion(rec); err != nil {
				t.Fatal(err)
			}
			statePath, _ := c.referencePath("state.json")
			before, _ := os.ReadFile(statePath)
			path, _ := c.recordPath(rec.Name)
			var release func()
			var err error
			switch mode {
			case "corrupt":
				err = os.WriteFile(path, []byte("{"), 0o600)
			case "legacy":
				rec.Version = 0
				err = c.Save(rec)
			case "symlink":
				err = os.Remove(path)
				if err == nil {
					err = os.Symlink(filepath.Join(t.TempDir(), "missing"), path)
				}
			case "held":
				release, err = c.lockSession(rec.Name, 0)
			case "unreadable":
				if os.Geteuid() == 0 {
					t.Skip("root can read files without read permission")
				}
				err = os.Chmod(path, 0)
			case "journal":
				journalPath, _ := c.referencePath("journal.json")
				err = os.WriteFile(journalPath, []byte("{"), 0o600)
			case "library":
				release, err = image.LockLibrary(c.ImagesDir, 0)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.EnsureInventory()
			if release != nil {
				release()
			}
			if err == nil || (mode == "held" && !errors.Is(err, ErrLockHeld)) ||
				(mode == "library" && !errors.Is(err, filelock.ErrHeld)) {
				t.Fatalf("unsafe rebuild accepted: %v", err)
			}
			if mode == "corrupt" || mode == "legacy" || mode == "symlink" || mode == "unreadable" {
				if !strings.Contains(err.Error(), path) {
					t.Fatalf("source path absent: %v", err)
				}
			}
			after, _ := os.ReadFile(statePath)
			if string(before) != string(after) {
				t.Fatal("rejected rebuild changed index state")
			}
		})
	}
}

func TestReferenceInventoryReconstructsUncertainEmptyState(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt", "dirty"} {
		c := newReferenceCoordinator(t)
		path, _ := c.referencePath("state.json")
		var err error
		switch mode {
		case "missing":
			err = os.Remove(path)
		case "corrupt":
			err = os.WriteFile(path, []byte("{"), 0o600)
		case "dirty":
			err = c.SaveReferenceState(ReferenceState{Version: ReferenceControlVersion, Generation: 2, Dirty: true})
		}
		if err != nil {
			t.Fatal(err)
		}
		state, err := c.EnsureInventory()
		if err != nil || state.Dirty || !state.CatalogComplete || len(state.Inventory) != 0 {
			t.Fatalf("%s rebuild = %#v, %v", mode, state, err)
		}
		forced, err := c.RebuildInventory()
		if err != nil || forced.Generation != state.Generation+1 || !forced.CatalogComplete {
			t.Fatalf("forced rebuild = %#v, %v", forced, err)
		}
	}
}
