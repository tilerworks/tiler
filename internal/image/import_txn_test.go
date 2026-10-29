package image

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportRecoveryUsesPhaseForIdenticalManifests(t *testing.T) {
	prepared := newImportCrash(t)
	prepared.save(t, ImportPrepared)
	if err := RecoverImportTxn(prepared.images, prepared.repo.Name()); err != nil {
		t.Fatal(err)
	}
	prepared.expectRolledBack(t)
	if err := RecoverImportTxn(prepared.images, prepared.repo.Name()); err != nil {
		t.Fatal(err)
	}

	committed := newImportCrash(t)
	committed.save(t, ImportCommitted)
	if err := RecoverImportTxn(committed.images, committed.repo.Name()); err != nil {
		t.Fatal(err)
	}
	committed.expectCleaned(t)
	if _, err := os.Stat(filepath.Join(committed.repo.Dir, fileSHA256Index)); err != nil {
		t.Fatal(err)
	}
}

func TestImportRecoveryInjectionTouchesOnlyOwnedPaths(t *testing.T) {
	fix := newImportCrash(t)
	fix.save(t, ImportPrepared)
	err := recoverImportTxn(fix.images, fix.repo.Name(), hooks{before: func(path, op string) error {
		if !importInside(fix.images, path) {
			t.Errorf("recovery touched %s", path)
		}
		if op == "backup" {
			return errors.New("injected")
		}
		return nil
	}})
	if !errors.Is(err, ErrImportCleanupPending) {
		t.Fatalf("recovery = %v", err)
	}
	fix.expectUntouched(t)
	if payloadText(t, fix.repo, fix.node) != "promoted-local\n" {
		t.Fatal("failed restore changed the promoted leaf")
	}
	if err := RecoverImportTxn(fix.images, fix.repo.Name()); err != nil {
		t.Fatal(err)
	}
	fix.expectRolledBack(t)
}

func TestImportRecoveryRollsBackMetadataAndRefusesPartialCommit(t *testing.T) {
	fix := newImportCrash(t)
	extra := fullNode(strings.Repeat("c", 64), fix.node.Size, fix.node.MtimeUnix)
	after := versionManifest(fix.repo, fix.node, fix.kept, extra)
	writeManifest(t, fix.repo, after)
	writeAbs(t, filepath.Join(fix.repo.Dir, dirObjects, extra.ID, "marker"), []byte("extra"))
	fix.saveWith(t, ImportTxn{
		Name: fix.repo.Name(), Phase: ImportPrepared, Before: fix.manifest, After: after,
		Staged: fix.staged, Objects: []string{extra.ID}, Backups: fix.backups,
	})
	if err := RecoverImportTxn(fix.images, fix.repo.Name()); err != nil {
		t.Fatal(err)
	}
	got, err := LoadVersionManifest(fix.repo)
	if err != nil || len(got.Images) != 2 || got.Images[1].ID != fix.kept.ID {
		t.Fatalf("manifest = %+v, %v", got.Images, err)
	}
	if _, err := os.Stat(filepath.Join(fix.repo.Dir, dirObjects, extra.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new object remains: %v", err)
	}
	fix.expectRolledBack(t)

	conflict := newImportCrash(t)
	conflict.saveWith(t, ImportTxn{
		Name: conflict.repo.Name(), Phase: ImportCommitted, Before: conflict.manifest,
		After:   versionManifest(conflict.repo, conflict.node, conflict.kept, extra),
		Backups: conflict.backups,
	})
	if err := RecoverImportTxn(conflict.images, conflict.repo.Name()); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflict = %v", err)
	}
	if _, err := os.Stat(filepath.Join(conflict.repo.Dir, filepath.FromSlash(conflict.backups[0].Saved))); err != nil {
		t.Fatalf("backup deleted on conflict: %v", err)
	}
	conflict.expectUntouched(t)
}

func TestImportRecoveryLeavesRemovalJournalAlone(t *testing.T) {
	fix := newImportCrash(t)
	fix.save(t, ImportPrepared)
	journal := mustControl(t, fix.images, fix.repo.Name(), versionRemovalJournal)
	if err := os.MkdirAll(filepath.Dir(journal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RecoverImportTxn(fix.images, fix.repo.Name()); err == nil || !strings.Contains(err.Error(), "removal") {
		t.Fatal(err)
	}
	if _, err := os.Stat(mustControl(t, fix.images, fix.repo.Name(), importTxnJournal)); err != nil {
		t.Fatal(err)
	}
	fix.expectUntouched(t)
	if err := SaveImportTxn(fix.images, fix.txn(ImportPrepared)); err == nil {
		t.Fatal("saved across a removal journal")
	}
}

func TestSaveImportTxnRejectsForeignPaths(t *testing.T) {
	fix := newImportCrash(t)
	txn := fix.txn(ImportPrepared)
	txn.Staged = append(txn.Staged, "../keep.txt")
	if err := SaveImportTxn(fix.images, txn); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatal(err)
	}
	txn = fix.txn(ImportCommitted)
	txn.Objects = []string{fix.node.ID}
	if err := SaveImportTxn(fix.images, txn); err == nil {
		t.Fatal("existing object accepted as new")
	}
	if _, err := os.Stat(mustControl(t, fix.images, fix.repo.Name(), importTxnJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected save wrote a journal: %v", err)
	}
}

type importCrash struct {
	images, external string
	repo             Repo
	node, kept       ImageMeta
	manifest         VersionManifest
	live, keptBody   []byte
	externalBody     []byte
	staged           []string
	backups          []ImportBackup
}

func newImportCrash(t *testing.T) importCrash {
	t.Helper()
	repo, node, src := linkedNode(t, strings.Repeat("a", 64), []byte("vendor-payload"), 50)
	kept := fullNode(strings.Repeat("b", 64), node.Size, node.MtimeUnix)
	keptPath, err := repo.ObjectPayloadPath(kept)
	if err != nil {
		t.Fatal(err)
	}
	keptBody := []byte("kept-local")
	writeAbs(t, keptPath, keptBody)
	manifest := versionManifest(repo, node, kept)
	writeManifest(t, repo, manifest)
	livePath, err := repo.ObjectPayloadPath(node)
	if err != nil {
		t.Fatal(err)
	}
	live := mustRead(t, livePath)
	saved := importBackupRel + "/previous"
	writeAbs(t, filepath.Join(repo.Dir, filepath.FromSlash(saved)), live)
	writeAbs(t, livePath, []byte("promoted-local\n"))
	staged := []string{importStageRel + "/copy", importStageRel + "/ext-link"}
	writeAbs(t, filepath.Join(repo.Dir, importStageRel, "copy"), []byte("staged"))
	if err := os.Symlink(src, filepath.Join(repo.Dir, importStageRel, "ext-link")); err != nil {
		t.Fatal(err)
	}
	images := libraryRoot(repo)
	writeAbs(t, filepath.Join(images, "keep.txt"), []byte("library-keep"))
	return importCrash{
		images: images, external: src, repo: repo, node: node, kept: kept,
		manifest: manifest, live: live, keptBody: keptBody, externalBody: mustRead(t, src),
		staged: staged, backups: []ImportBackup{{
			Live: filepath.ToSlash(mustRel(t, repo.Dir, livePath)), Saved: saved,
		}},
	}
}

func (fix importCrash) txn(phase ImportPhase) ImportTxn {
	return ImportTxn{
		Name: fix.repo.Name(), Phase: phase, Before: fix.manifest, After: fix.manifest,
		Staged: fix.staged, Objects: []string{strings.Repeat("d", 64)}, Backups: fix.backups,
	}
}

func (fix importCrash) save(t *testing.T, phase ImportPhase) {
	t.Helper()
	stray := filepath.Join(fix.repo.Dir, dirObjects, strings.Repeat("d", 64), "marker")
	writeAbs(t, stray, []byte("stray"))
	fix.saveWith(t, fix.txn(phase))
}

func (fix importCrash) saveWith(t *testing.T, txn ImportTxn) {
	t.Helper()
	if err := SaveImportTxn(fix.images, txn); err != nil {
		t.Fatal(err)
	}
}

func (fix importCrash) expectRolledBack(t *testing.T) {
	t.Helper()
	if payloadText(t, fix.repo, fix.node) != string(fix.live) {
		t.Fatal("link was not restored")
	}
	if _, err := os.Stat(filepath.Join(fix.repo.Dir, dirObjects, strings.Repeat("d", 64))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stray object remains: %v", err)
	}
	fix.expectCommon(t, false)
}

func (fix importCrash) expectCleaned(t *testing.T) {
	t.Helper()
	if payloadText(t, fix.repo, fix.node) != "promoted-local\n" {
		t.Fatal("committed conversion restored the link")
	}
	marker := filepath.Join(fix.repo.Dir, dirObjects, strings.Repeat("d", 64), "marker")
	if string(mustRead(t, marker)) != "stray" {
		t.Fatal("cleanup removed a published object")
	}
	fix.expectCommon(t, true)
}

func (fix importCrash) expectCommon(t *testing.T, committed bool) {
	t.Helper()
	for _, rel := range fix.staged {
		if _, err := os.Lstat(filepath.Join(fix.repo.Dir, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("staged %s remains: %v", rel, err)
		}
	}
	_, err := os.Lstat(filepath.Join(fix.repo.Dir, filepath.FromSlash(fix.backups[0].Saved)))
	if committed && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup remains: %v", err)
	}
	if !committed && err == nil {
		t.Fatal("rollback left the backup in place")
	}
	if _, err := os.Stat(mustControl(t, fix.images, fix.repo.Name(), importTxnJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains: %v", err)
	}
	fix.expectUntouched(t)
}

func (fix importCrash) expectUntouched(t *testing.T) {
	t.Helper()
	keptPath, err := fix.repo.ObjectPayloadPath(fix.kept)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustRead(t, keptPath), fix.keptBody) || !bytes.Equal(mustRead(t, fix.external), fix.externalBody) {
		t.Fatal("recovery changed an existing object or external source")
	}
	if string(mustRead(t, filepath.Join(fix.images, "keep.txt"))) != "library-keep" {
		t.Fatal("recovery changed an unlisted library file")
	}
}

func writeManifest(t *testing.T, repo Repo, manifest VersionManifest) {
	t.Helper()
	body, err := EncodeVersionManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeAbs(t, repo.MetaPath(), body)
}

func writeAbs(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRel(t *testing.T, root, abs string) string {
	t.Helper()
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func importInside(root, path string) bool {
	clean := filepath.Clean(path)
	root = filepath.Clean(root)
	return clean == root || strings.HasPrefix(clean, root+string(filepath.Separator))
}
