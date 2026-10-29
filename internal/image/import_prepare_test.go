package image

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareTargetReplaysRemovalBeforeACleanLibrary(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	journal := removalJournal{
		Name: repo.Name(), Before: removalManifest(repo, nodes),
		After: removalManifest(repo, nodes[:1]), IDs: []string{nodes[2].ID, nodes[1].ID},
	}
	if err := saveRemovalJournal(imagesDir, journal, hooks{}); err != nil {
		t.Fatal(err)
	}
	if err := PrepareTargetLocked(imagesDir); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 1 || manifest.Images[0].ID != nodes[0].ID {
		t.Fatalf("manifest = %+v, %v", manifest.Images, err)
	}
}

func TestPrepareTargetRepeatsAndLeavesOtherLibraries(t *testing.T) {
	fix := newImportCrash(t)
	fix.save(t, ImportPrepared)
	source := t.TempDir()
	writeAbs(t, filepath.Join(source, importTxnJournal), []byte("{"))
	writeAbs(t, filepath.Join(source, "payload"), []byte("nas"))
	if err := PrepareTargetLocked(fix.images); err != nil {
		t.Fatal(err)
	}
	fix.expectRolledBack(t)
	if err := PrepareTargetLocked(fix.images); err != nil {
		t.Fatal(err)
	}
	fix.expectRolledBack(t)
	if string(mustRead(t, filepath.Join(source, importTxnJournal))) != "{" ||
		string(mustRead(t, filepath.Join(source, "payload"))) != "nas" {
		t.Fatal("target recovery changed another library")
	}
}

func TestPreparedReadStopsWhileCleanupIsPending(t *testing.T) {
	fix := newImportCrash(t)
	fix.save(t, ImportCommitted)
	previous := finishPreparedImport
	t.Cleanup(func() { finishPreparedImport = previous })
	finishPreparedImport = func(imagesDir string) error {
		return recoverImportTxn(imagesDir, hooks{before: func(_ string, op string) error {
			if op == "journal" {
				return errors.New("injected")
			}
			return nil
		}})
	}
	called := false
	err := WithPreparedTarget(fix.images, func() error {
		called = true
		_, err := LoadVersionManifest(fix.repo)
		return err
	})
	if !errors.Is(err, ErrImportCleanupPending) || called {
		t.Fatalf("read = %v, called %v", err, called)
	}
	if !bytes.Equal(mustRead(t, fix.external), fix.externalBody) {
		t.Fatal("pending cleanup changed the external source")
	}
}

func TestPreparedReadWaitsForTheLibraryLock(t *testing.T) {
	fix := newImportCrash(t)
	fix.save(t, ImportPrepared)
	release, err := LockLibrary(fix.images, 0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- WithPreparedTarget(fix.images, func() error { return nil })
	}()
	select {
	case err := <-done:
		release()
		t.Fatalf("read did not wait: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(fix.images, importTxnJournal)); err != nil {
		release()
		t.Fatalf("recovery ran without the lock: %v", err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read stuck after the lock was released")
	}
	fix.expectRolledBack(t)
}

func TestPrepareTargetFinishesRemovalBeforeImport(t *testing.T) {
	fix := newImportCrash(t)
	fix.save(t, ImportPrepared)
	journal := filepath.Join(fix.images, versionRemovalJournal)
	if err := os.WriteFile(journal, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareTargetLocked(fix.images); err == nil {
		t.Fatal("broken removal journal was ignored")
	}
	if _, err := os.Stat(filepath.Join(fix.images, importTxnJournal)); err != nil {
		t.Fatal(err)
	}
	fix.expectUntouched(t)
	if err := saveRemovalJournal(fix.images, removalJournal{Name: fix.repo.Name()}, hooks{}); err == nil {
		t.Fatal("removal journal replaced a pending import")
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if err := PrepareTargetLocked(fix.images); err != nil {
		t.Fatal(err)
	}
	fix.expectRolledBack(t)

	_, err := UpdateVersionDescription(fix.images, fix.repo.Name(), fix.kept.ID, "keep", "next")
	if err != nil {
		t.Fatal(err)
	}
	if payloadText(t, fix.repo, fix.node) != string(fix.live) {
		t.Fatal("mutation rewrote the restored link")
	}
}
