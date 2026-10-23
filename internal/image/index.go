package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	fileSHA256Index        = "sha256.index"
	fileContentSHA256Index = "content_sha256.index"
	// indexRelActive is the relative path of the active leaf root.
	indexRelActive = "."
)

// errMalformedIndex means a leaf index cannot be searched. Lookup
// treats that as stale and rebuilds once.
var errMalformedIndex = errors.New("malformed index")

// indexRow is one sorted index line: a 64-hex key and a leaf-relative
// meta root.
type indexRow struct {
	key string
	rel string
}

// indexField selects which meta hash a row must still match.
type indexField int

const (
	indexPayload indexField = iota
	indexContent
)

type probeStatus int

const (
	probeOK probeStatus = iota
	probeStale
	probeAmbiguous
)

// indexLookup is one search of a leaf index.
type indexLookup struct {
	file   string
	ref    string
	field  indexField
	prefix bool
	idRef  bool
}

// ensureIndexes writes both leaf indexes when either file is missing.
// Existing files are left alone; lookup rebuilds a stale index.
func ensureIndexes(repo Repo) error {
	if indexesPresent(repo) {
		return nil
	}
	return rebuildIndexes(repo)
}

func indexesPresent(repo Repo) bool {
	for _, name := range []string{fileSHA256Index, fileContentSHA256Index} {
		info, err := os.Stat(filepath.Join(repo.Dir, name))
		if err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

// rebuildIndexes scans readable metas and replaces both indexes.
// Unreadable hold and archive directories are skipped. Each file is
// written to a temp name and renamed into place.
func rebuildIndexes(repo Repo) error {
	shaRows, contentRows, err := scanIndexRows(repo)
	if err != nil {
		return err
	}
	if err := writeIndex(repo.Dir, fileSHA256Index, shaRows); err != nil {
		return err
	}
	return writeIndex(repo.Dir, fileContentSHA256Index, contentRows)
}

func scanIndexRows(repo Repo) ([]indexRow, []indexRow, error) {
	var shaRows, contentRows []indexRow
	if err := appendActiveMeta(repo, &shaRows, &contentRows); err != nil {
		return nil, nil, err
	}
	appendChainMetas(repo, dirHold, &shaRows, &contentRows)
	appendChainMetas(repo, dirArchives, &shaRows, &contentRows)
	sortIndexRows(shaRows)
	sortIndexRows(contentRows)
	return shaRows, contentRows, nil
}

func appendActiveMeta(repo Repo, shaRows, contentRows *[]indexRow) error {
	meta, err := Load(repo.MetaPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	appendMetaRow(meta, indexRelActive, shaRows, contentRows)
	return nil
}

// appendChainMetas adds every readable chain meta under kind.
// A missing or unreadable directory, and an unreadable child, are
// ignored so one bad hold cannot block the index.
func appendChainMetas(repo Repo, kind string, shaRows, contentRows *[]indexRow) {
	entries, err := os.ReadDir(filepath.Join(repo.Dir, kind))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !onDiskID(entry.Name()) {
			continue
		}
		dir := filepath.Join(repo.Dir, kind, entry.Name())
		meta, err := Load(filepath.Join(dir, fileMeta))
		if err != nil {
			continue
		}
		rel := kind + "/" + entry.Name()
		appendMetaRow(meta, rel, shaRows, contentRows)
	}
}

func appendMetaRow(meta Meta, rel string, shaRows, contentRows *[]indexRow) {
	*shaRows = append(*shaRows, indexRow{key: meta.SHA256, rel: rel})
	if meta.ContentSHA256 != "" {
		row := indexRow{key: meta.ContentSHA256, rel: rel}
		*contentRows = append(*contentRows, row)
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

func writeIndex(dir, name string, rows []indexRow) error {
	tmpName, err := tempName(name)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, tmpName)
	if err := writeSynced(tmp, encodeIndex(rows)); err != nil {
		removeFile(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		removeFile(tmp)
		return err
	}
	return nil
}

func encodeIndex(rows []indexRow) []byte {
	var buf strings.Builder
	for _, row := range rows {
		buf.WriteString(row.key)
		buf.WriteByte(' ')
		buf.WriteString(row.rel)
		buf.WriteByte('\n')
	}
	return []byte(buf.String())
}

// lookupSHA256 returns the leaf-relative root whose payload sha256
// is sum. A missing index, a missing key, or a stale path rebuilds
// both indexes once. A second miss is not found.
func lookupSHA256(repo Repo, sum string) (string, error) {
	if !isLowerHex(sum, sumHexLen) {
		return "", invalidPath(sum)
	}
	return lookupIndexed(repo, indexLookup{
		file: fileSHA256Index, ref: sum, field: indexPayload,
	})
}

// lookupContentSHA256 returns the leaf-relative root whose
// content_sha256 is sum. Stale rows rebuild both indexes once.
func lookupContentSHA256(repo Repo, sum string) (string, error) {
	if !isLowerHex(sum, sumHexLen) {
		return "", invalidPath(sum)
	}
	return lookupIndexed(repo, indexLookup{
		file: fileContentSHA256Index, ref: sum, field: indexContent,
	})
}

// lookupID returns the single sha256.index row whose key has idRef
// as a prefix. Zero matches is not found. More than one fresh match
// is an ambiguity error. Both errors quote idRef.
func lookupID(repo Repo, idRef string) (string, error) {
	if !isIDRef(idRef) {
		return "", invalidPath(idRef)
	}
	return lookupIndexed(repo, indexLookup{
		file:   fileSHA256Index,
		ref:    idRef,
		field:  indexPayload,
		prefix: true,
		idRef:  true,
	})
}

// FindRepoByID locates the single library leaf whose sha256.index has
// a unique prefix match for idRef. idRef is the full 64-hex payload
// sha256 or a unique lowercase hex prefix. Zero matches and matches
// in more than one leaf quote idRef.
func FindRepoByID(imagesDir, idRef string) (Repo, error) {
	if !isIDRef(idRef) {
		return Repo{}, fmt.Errorf("unknown id reference %q", idRef)
	}
	repos, err := ListRepos(imagesDir)
	if err != nil {
		return Repo{}, err
	}
	var match Repo
	var matchKey string
	for _, repo := range repos {
		rel, err := lookupID(repo, idRef)
		switch {
		case err == nil:
			meta, loadErr := Load(indexMetaPath(repo, rel))
			if loadErr != nil {
				return Repo{}, loadErr
			}
			if matchKey != "" && (matchKey != meta.SHA256 || match.Name() != repo.Name()) {
				return Repo{}, fmt.Errorf("ambiguous id reference %q", idRef)
			}
			match, matchKey = repo, meta.SHA256
		case isUnknownIDRef(err):
			continue
		default:
			return Repo{}, err
		}
	}
	if matchKey == "" {
		return Repo{}, fmt.Errorf("unknown id reference %q", idRef)
	}
	return match, nil
}

func lookupIndexed(repo Repo, query indexLookup) (string, error) {
	rel, status, err := probeIndex(repo, query)
	if err != nil || status != probeStale {
		return finishProbe(rel, status, err, query)
	}
	if err := rebuildIndexes(repo); err != nil {
		return "", err
	}
	rel, status, err = probeIndex(repo, query)
	if err != nil || status != probeStale {
		return finishProbe(rel, status, err, query)
	}
	return "", indexMiss(query)
}

func finishProbe(rel string, status probeStatus, err error, query indexLookup) (string, error) {
	if err != nil {
		return "", err
	}
	switch status {
	case probeOK:
		return rel, nil
	case probeAmbiguous:
		return "", indexAmbiguous(query)
	default:
		return "", indexMiss(query)
	}
}

func indexMiss(query indexLookup) error {
	switch {
	case query.file == fileContentSHA256Index:
		return fmt.Errorf("content_sha256 %q: not found", query.ref)
	case query.idRef:
		return fmt.Errorf("unknown id reference %q", query.ref)
	default:
		return fmt.Errorf("sha256 %q: not found", query.ref)
	}
}

func indexAmbiguous(query indexLookup) error {
	switch {
	case query.idRef:
		return fmt.Errorf("ambiguous id reference %q", query.ref)
	case query.file == fileContentSHA256Index:
		return fmt.Errorf("ambiguous content_sha256 %q", query.ref)
	default:
		return fmt.Errorf("ambiguous sha256 %q", query.ref)
	}
}

func probeIndex(repo Repo, query indexLookup) (string, probeStatus, error) {
	rows, err := readIndex(filepath.Join(repo.Dir, query.file))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, errMalformedIndex) {
		return "", probeStale, nil
	}
	if err != nil {
		return "", probeStale, err
	}
	matches := matchIndex(rows, query.ref, query.prefix)
	rel, status := classifyMatches(repo, matches, query.field)
	return rel, status, nil
}

func matchIndex(rows []indexRow, ref string, prefix bool) []indexRow {
	if prefix {
		return spanMatches(rows, ref, func(key string) bool {
			return strings.HasPrefix(key, ref)
		})
	}
	return spanMatches(rows, ref, func(key string) bool {
		return key == ref
	})
}

func spanMatches(rows []indexRow, start string, keep func(string) bool) []indexRow {
	i := sort.Search(len(rows), func(i int) bool {
		return rows[i].key >= start
	})
	var out []indexRow
	for ; i < len(rows) && keep(rows[i].key); i++ {
		out = append(out, rows[i])
	}
	return out
}

func classifyMatches(repo Repo, matches []indexRow, field indexField) (string, probeStatus) {
	if len(matches) == 0 {
		return "", probeStale
	}
	for _, row := range matches {
		if !rowFresh(repo, row, field) {
			return "", probeStale
		}
	}
	if len(matches) > 1 {
		return "", probeAmbiguous
	}
	return matches[0].rel, probeOK
}

func rowFresh(repo Repo, row indexRow, field indexField) bool {
	meta, err := Load(indexMetaPath(repo, row.rel))
	if err != nil {
		return false
	}
	if field == indexContent {
		return meta.ContentSHA256 == row.key
	}
	return meta.SHA256 == row.key
}

func indexMetaPath(repo Repo, rel string) string {
	if rel == indexRelActive {
		return repo.MetaPath()
	}
	return filepath.Join(repo.Dir, filepath.FromSlash(rel), fileMeta)
}

func readIndex(path string) ([]indexRow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseIndex(data)
}

func parseIndex(data []byte) ([]indexRow, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, errMalformedIndex
	}
	text := string(data[:len(data)-1])
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	rows := make([]indexRow, 0, len(lines))
	var prev string
	for i, line := range lines {
		row, err := parseIndexLine(line)
		if err != nil {
			return nil, err
		}
		if i > 0 && row.key < prev {
			return nil, errMalformedIndex
		}
		prev = row.key
		rows = append(rows, row)
	}
	return rows, nil
}

func parseIndexLine(line string) (indexRow, error) {
	key, rel, ok := strings.Cut(line, " ")
	if !ok || strings.Contains(rel, " ") || !isLowerHex(key, sumHexLen) || !validIndexRel(rel) {
		return indexRow{}, errMalformedIndex
	}
	return indexRow{key: key, rel: rel}, nil
}

func validIndexRel(rel string) bool {
	if rel == indexRelActive {
		return true
	}
	kind, id, ok := strings.Cut(rel, "/")
	if !ok || strings.Contains(id, "/") {
		return false
	}
	if kind != dirHold && kind != dirArchives {
		return false
	}
	return onDiskID(id)
}
