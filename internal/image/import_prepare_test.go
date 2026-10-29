package image

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareImageClaimsLegacyRemovalJournal(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	journal := removalJournal{
		Name: repo.Name(), Before: removalManifest(repo, nodes),
		After: removalManifest(repo, nodes[:1]), IDs: []string{nodes[2].ID, nodes[1].ID},
	}
	body, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imagesDir, versionRemovalJournal), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareImageLocked(imagesDir, "bob/docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, versionRemovalJournal)); err != nil {
		t.Fatal("another image claimed the legacy journal")
	}
	if err := PrepareImageLocked(imagesDir, repo.Name()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, versionRemovalJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy journal remains: %v", err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 1 || manifest.Images[0].ID != nodes[0].ID {
		t.Fatalf("manifest = %+v, %v", manifest.Images, err)
	}
}

func TestPrepareTargetReplaysRemovalBeforeACleanLibrary(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	journal := removalJournal{
		Name: repo.Name(), Before: removalManifest(repo, nodes),
		After: removalManifest(repo, nodes[:1]), IDs: []string{nodes[2].ID, nodes[1].ID},
	}
	if err := saveRemovalJournal(imagesDir, journal, hooks{}); err != nil {
		t.Fatal(err)
	}
	if err := PrepareImageLocked(imagesDir, repo.Name()); err != nil {
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
	if err := PrepareImageLocked(fix.images, fix.repo.Name()); err != nil {
		t.Fatal(err)
	}
	fix.expectRolledBack(t)
	if err := PrepareImageLocked(fix.images, fix.repo.Name()); err != nil {
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
	finishPreparedImport = func(imagesDir, name string) error {
		return recoverImportTxn(imagesDir, name, hooks{before: func(_ string, op string) error {
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
	if err != nil || !called {
		t.Fatalf("unlocked read = %v, called %v", err, called)
	}
	if err := PrepareImageLocked(fix.images, fix.repo.Name()); !errors.Is(err, ErrImportCleanupPending) {
		t.Fatalf("prepare = %v", err)
	}
	if !bytes.Equal(mustRead(t, fix.external), fix.externalBody) {
		t.Fatal("pending cleanup changed the external source")
	}
}

func TestPreparedReadWaitsForTheLibraryLock(t *testing.T) {
	fix := newImportCrash(t)
	fix.save(t, ImportPrepared)
	hold, err := LockImage(fix.images, fix.repo.Name(), 0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- WithPreparedTarget(fix.images, func() error { return nil })
	}()
	select {
	case err := <-done:
		if err != nil {
			hold.Release()
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		hold.Release()
		t.Fatal("read waited for an image lock")
	}
	journal, err := controlFile(fix.images, fix.repo.Name(), importTxnJournal)
	if err != nil {
		hold.Release()
		t.Fatal(err)
	}
	if _, err := os.Stat(journal); err != nil {
		hold.Release()
		t.Fatalf("read replayed the journal: %v", err)
	}
	hold.Release()
}

func TestPrepareTargetFinishesRemovalBeforeImport(t *testing.T) {
	fix := newImportCrash(t)
	fix.save(t, ImportPrepared)
	journal, err := controlFile(fix.images, fix.repo.Name(), versionRemovalJournal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(journal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := PrepareImageLocked(fix.images, fix.repo.Name()); err == nil {
		t.Fatal("broken removal journal was ignored")
	}
	importJournal, err := controlFile(fix.images, fix.repo.Name(), importTxnJournal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(importJournal); err != nil {
		t.Fatal(err)
	}
	fix.expectUntouched(t)
	if err := saveRemovalJournal(fix.images, removalJournal{Name: fix.repo.Name()}, hooks{}); err == nil {
		t.Fatal("removal journal replaced a pending import")
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if err := PrepareImageLocked(fix.images, fix.repo.Name()); err != nil {
		t.Fatal(err)
	}
	fix.expectRolledBack(t)

	_, err = UpdateVersionDescription(fix.images, fix.repo.Name(), fix.kept.ID, "keep", "next")
	if err != nil {
		t.Fatal(err)
	}
	if payloadText(t, fix.repo, fix.node) != string(fix.live) {
		t.Fatal("mutation rewrote the restored link")
	}
}
