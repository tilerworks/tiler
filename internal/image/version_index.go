package image

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
)

const fileVersionRevision = "meta.sha256.index"

// EnsureVersionIndexes refreshes derived indexes from all committed v3 nodes.
// Callers must hold the library lock. The returned revision includes mutable
// metadata such as descriptions; no cache file is a source of version identity.
func EnsureVersionIndexes(repo Repo) (string, error) {
	manifest, body, err := loadVersionMeta(repo)
	if err != nil {
		return "", err
	}
	if body == nil {
		return "", os.ErrNotExist
	}
	var payload, content []indexRow
	for _, node := range manifest.Images {
		appendIndexRow(node.SHA256, node.ContentSHA256, dirObjects+"/"+node.ID, &payload, &content)
	}
	sortIndexRows(payload)
	sortIndexRows(content)
	revision := ID(sha256.Sum256(body))
	for _, index := range []struct {
		name string
		rows []indexRow
	}{
		{fileSHA256Index, payload},
		{fileContentSHA256Index, content},
		{fileVersionRevision, []indexRow{{key: revision, rel: indexRelActive}}},
	} {
		if err := refreshVersionIndex(repo, index.name, index.rows); err != nil {
			return "", err
		}
	}
	return revision, nil
}

func refreshVersionIndex(repo Repo, name string, rows []indexRow) error {
	path := filepath.Join(repo.Dir, name)
	if err := repo.checkObjectComponents(path, false); err != nil {
		return err
	}
	cached, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && bytes.Equal(cached, encodeIndex(rows)) {
		return nil
	}
	return writeIndex(repo.Dir, name, rows)
}
