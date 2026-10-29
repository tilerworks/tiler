package image

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
)

func validImportTxn(repo Repo, txn ImportTxn) error {
	if txn.Phase != ImportPrepared && txn.Phase != ImportCommitted {
		return errors.New("import: invalid transaction")
	}
	if txn.Before.Name != repo.Name() || txn.After.Name != repo.Name() {
		return errors.New("import: invalid transaction")
	}
	if _, err := EncodeVersionManifest(txn.Before); err != nil {
		return fmtInvalid(err)
	}
	if _, err := EncodeVersionManifest(txn.After); err != nil {
		return fmtInvalid(err)
	}
	return validImportPaths(repo, txn)
}

func fmtInvalid(err error) error {
	if err == nil {
		return nil
	}
	return errors.New("import: invalid transaction")
}

func validImportPaths(repo Repo, txn ImportTxn) error {
	seen := map[string]bool{}
	ids := manifestIDs(txn.Before)
	for _, rel := range txn.Staged {
		if err := claimImportPath(repo, seen, rel, cleanRel(rel) && under(rel, importStageRel)); err != nil {
			return err
		}
	}
	if err := claimImportObjects(repo, seen, ids, txn.Objects); err != nil {
		return err
	}
	for _, backup := range txn.Backups {
		if err := claimImportBackup(repo, seen, ids, backup); err != nil {
			return err
		}
	}
	return nil
}

func claimImportObjects(repo Repo, seen, ids map[string]bool, objects []string) error {
	for _, id := range objects {
		if ids[id] || seen["id:"+id] || checkID(id) != nil {
			return errors.New("import: invalid transaction")
		}
		if _, err := repo.ObjectDirPath(id); err != nil {
			return errors.New("import: invalid transaction")
		}
		seen["id:"+id] = true
	}
	return nil
}

func claimImportBackup(repo Repo, seen, ids map[string]bool, backup ImportBackup) error {
	ok := backup.Live != backup.Saved && cleanRel(backup.Live) && cleanRel(backup.Saved)
	ok = ok && validImportLive(backup.Live, ids) && under(backup.Saved, importBackupRel)
	if err := claimImportPath(repo, seen, backup.Live, ok); err != nil {
		return err
	}
	return claimImportPath(repo, seen, backup.Saved, ok)
}

func claimImportPath(repo Repo, seen map[string]bool, rel string, ok bool) error {
	if !ok || seen[rel] {
		return errors.New("import: invalid transaction")
	}
	if _, err := ownedImportPath(repo, rel); err != nil {
		return err
	}
	seen[rel] = true
	return nil
}

func validImportLive(rel string, ids map[string]bool) bool {
	parts := strings.Split(rel, "/")
	return len(parts) >= 3 && parts[0] == dirObjects && ids[parts[1]] && checkID(parts[1]) == nil
}

func manifestIDs(manifest VersionManifest) map[string]bool {
	ids := make(map[string]bool, len(manifest.Images))
	for _, node := range manifest.Images {
		ids[node.ID] = true
	}
	return ids
}

func cleanRel(rel string) bool {
	return rel != "" && !strings.Contains(rel, `\`) && !path.IsAbs(rel) && path.Clean(rel) == rel
}

func under(rel, dir string) bool {
	return strings.HasPrefix(rel, dir+"/")
}

func ownedImportPath(repo Repo, rel string) (string, error) {
	if !cleanRel(rel) {
		return "", errors.New("import: invalid transaction path")
	}
	abs := filepath.Join(repo.Dir, filepath.FromSlash(rel))
	root := filepath.Clean(repo.Dir)
	clean := filepath.Clean(abs)
	if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return "", errors.New("import: invalid transaction path")
	}
	return clean, nil
}
