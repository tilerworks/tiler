package image

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	fileSHA256Index        = "sha256.index"
	fileContentSHA256Index = "content_sha256.index"
	fileVersionRevision    = "meta.sha256.index"
	indexRelActive         = "."
)

type indexRow struct {
	key string
	rel string
}

// appendIndexRow records digest keys for one object. The reserved
// unknown marker is not a content address, so it is left out of both
// derived indexes and cannot join two image names. An empty content
// digest stays omitted.
func appendIndexRow(sha, content, rel string, shaRows, contentRows *[]indexRow) {
	if sha != "" && !IsUnknownSHA256(sha) {
		*shaRows = append(*shaRows, indexRow{key: sha, rel: rel})
	}
	if content != "" && !IsUnknownSHA256(content) {
		*contentRows = append(*contentRows, indexRow{key: content, rel: rel})
	}
}

func sortIndexRows(rows []indexRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].key != rows[j].key {
			return rows[i].key < rows[j].key
		}
		return rows[i].rel < rows[j].rel
	})
}

func encodeIndex(rows []indexRow) []byte {
	var body strings.Builder
	for _, row := range rows {
		body.WriteString(row.key)
		body.WriteByte(' ')
		body.WriteString(row.rel)
		body.WriteByte('\n')
	}
	return []byte(body.String())
}

func writeIndex(dir, name string, rows []indexRow) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := writeTemp(dir, name, encodeIndex(rows), hooks{})
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		removeFile(tmp)
		return err
	}
	return syncDir(dir, hooks{})
}

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
