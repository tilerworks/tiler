package image

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RecoverImportTxn finishes or rolls back one image's import journal.
// A missing journal is success. A pending version-removal journal for
// that image is left untouched so the two recoveries do not interleave.
// The caller holds the image lock. External sources are never opened.
func RecoverImportTxn(imagesDir, name string) error {
	if err := claimLegacyJournals(imagesDir, name); err != nil {
		return err
	}
	return recoverImportTxn(imagesDir, name, hooks{})
}

func recoverImportTxn(imagesDir, name string, seam hooks) error {
	if err := importRemovalPending(imagesDir, name); err != nil {
		return err
	}
	txn, ok, err := loadImportTxn(imagesDir, name)
	if err != nil || !ok {
		return err
	}
	repo, err := OpenRepo(imagesDir, txn.Name)
	if err != nil {
		return err
	}
	if err := validImportTxn(repo, txn); err != nil {
		return err
	}
	if txn.Phase == ImportCommitted {
		return cleanupImportTxn(imagesDir, repo, txn, seam)
	}
	return rollbackImportTxn(imagesDir, repo, txn, seam)
}

func rollbackImportTxn(imagesDir string, repo Repo, txn ImportTxn, seam hooks) error {
	if err := restoreImportBackups(repo, txn.Backups, seam); err != nil {
		return pendingImport(err)
	}
	if err := restoreImportManifest(repo, txn.Before, seam); err != nil {
		return pendingImport(err)
	}
	if err := removeImportObjects(repo, txn.Objects, seam); err != nil {
		return pendingImport(err)
	}
	if err := removeImportRels(repo, txn.Staged, seam, "staged"); err != nil {
		return pendingImport(err)
	}
	return finishImportWork(imagesDir, repo, seam)
}

func cleanupImportTxn(imagesDir string, repo Repo, txn ImportTxn, seam hooks) error {
	match, err := manifestMatches(repo, txn.After)
	if err != nil || !match {
		return errors.Join(err, errors.New("import: transaction conflicts with metadata"))
	}
	if err := removeImportRels(repo, txn.Staged, seam, "staged"); err != nil {
		return pendingImport(err)
	}
	saved := make([]string, len(txn.Backups))
	for i, backup := range txn.Backups {
		saved[i] = backup.Saved
	}
	if err := removeImportRels(repo, saved, seam, "backup"); err != nil {
		return pendingImport(err)
	}
	return finishImportWork(imagesDir, repo, seam)
}

func finishImportWork(imagesDir string, repo Repo, seam hooks) error {
	if err := refreshImportIndexes(repo, seam); err != nil {
		return pendingImport(err)
	}
	if err := removeEmptyImportDir(filepath.Join(repo.Dir, importBackupRel)); err != nil {
		return pendingImport(err)
	}
	if err := removeEmptyImportDir(filepath.Join(repo.Dir, importStageRel)); err != nil {
		return pendingImport(err)
	}
	if err := syncExisting(repo.Dir); err != nil {
		return pendingImport(err)
	}
	return finishImportTxn(imagesDir, repo.Name(), seam)
}

// removeEmptyImportDir drops a transaction directory after its files are
// gone. A directory that still has entries stays; those entries are not
// necessarily owned by this journal.
func removeEmptyImportDir(dir string) error {
	err := os.Remove(dir)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, statErr := os.Lstat(dir); statErr == nil {
		return nil
	}
	return err
}

func restoreImportBackups(repo Repo, backups []ImportBackup, seam hooks) error {
	for _, backup := range backups {
		if err := restoreImportBackup(repo, backup, seam); err != nil {
			return err
		}
	}
	return nil
}

func restoreImportBackup(repo Repo, backup ImportBackup, seam hooks) error {
	saved, err := ownedImportPath(repo, backup.Saved)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(saved); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	live, err := ownedImportPath(repo, backup.Live)
	if err != nil {
		return err
	}
	if err := seam.guard(saved, "backup"); err != nil {
		return err
	}
	return replaceWithBackup(live, saved)
}

func replaceWithBackup(live, saved string) error {
	info, err := os.Lstat(live)
	if err == nil && info.IsDir() {
		return errors.New("import: backup target is a directory")
	}
	if err == nil {
		if err = os.Remove(live); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(saved, live); err != nil {
		return err
	}
	return syncDir(filepath.Dir(live), hooks{})
}

func restoreImportManifest(repo Repo, before VersionManifest, seam hooks) error {
	match, err := manifestMatches(repo, before)
	if err != nil || match {
		return err
	}
	// A first import records an empty before snapshot. The metadata file
	// did not exist yet, so rollback must not create one.
	if _, statErr := os.Lstat(repo.MetaPath()); errors.Is(statErr, os.ErrNotExist) && len(before.Images) == 0 {
		return nil
	}
	if err := seam.guard(repo.MetaPath(), "metadata"); err != nil {
		return err
	}
	body, err := EncodeVersionManifest(before)
	if err != nil {
		return err
	}
	current, readErr := os.ReadFile(repo.MetaPath())
	if errors.Is(readErr, os.ErrNotExist) {
		current = nil
	} else if readErr != nil {
		return readErr
	}
	return replaceVersionMeta(repo, current, body, hooks{})
}

func manifestMatches(repo Repo, want VersionManifest) (bool, error) {
	body, err := os.ReadFile(repo.MetaPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	got, err := DecodeVersionManifest(body)
	if err != nil {
		return false, nil
	}
	gotBody, err := EncodeVersionManifest(got)
	if err != nil {
		return false, err
	}
	wantBody, err := EncodeVersionManifest(want)
	if err != nil {
		return false, err
	}
	return bytes.Equal(gotBody, wantBody), nil
}

func removeImportObjects(repo Repo, ids []string, seam hooks) error {
	for _, id := range ids {
		dir, err := repo.ObjectDirPath(id)
		if err != nil {
			return err
		}
		if err := removeOwned(dir, seam, "object"); err != nil {
			return err
		}
	}
	return nil
}

func removeImportRels(repo Repo, rels []string, seam hooks, op string) error {
	for _, rel := range rels {
		abs, err := ownedImportPath(repo, rel)
		if err != nil {
			return err
		}
		if err := removeOwned(abs, seam, op); err != nil {
			return err
		}
	}
	return nil
}

func removeOwned(path string, seam hooks, op string) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := seam.guard(path, op); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func refreshImportIndexes(repo Repo, seam hooks) error {
	if err := seam.guard(repo.Dir, "index"); err != nil {
		return err
	}
	if _, err := os.Lstat(repo.MetaPath()); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	_, err := EnsureVersionIndexes(repo)
	return err
}

func finishImportTxn(imagesDir, name string, seam hooks) error {
	journal, err := controlFile(imagesDir, name, importTxnJournal)
	if err != nil {
		return pendingImport(err)
	}
	if err := seam.guard(journal, "journal"); err != nil {
		return pendingImport(err)
	}
	if err := os.Remove(journal); err != nil && !errors.Is(err, os.ErrNotExist) {
		return pendingImport(err)
	}
	removedLegacy := false
	legacy := filepath.Join(imagesDir, importTxnJournal)
	if journalName(legacy) == name {
		if err := os.Remove(legacy); err != nil && !errors.Is(err, os.ErrNotExist) {
			return pendingImport(err)
		}
		removedLegacy = true
	}
	if err := syncExisting(filepath.Dir(journal)); err != nil {
		return pendingImport(err)
	}
	if removedLegacy {
		return pendingImport(syncDir(imagesDir, hooks{}))
	}
	return nil
}

func syncExisting(dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return syncDir(dir, hooks{})
}

func pendingImport(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrImportCleanupPending, err)
}

func (seam hooks) guard(path, op string) error {
	if seam.before == nil {
		return nil
	}
	return seam.before(path, op)
}
