package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRebuildIndexesWritesSortedFiles(t *testing.T) {
	repo := indexLeaf(t)
	active := strings.Repeat("f", sumHexLen)
	hold := strings.Repeat("1", sumHexLen)
	archive := strings.Repeat("8", sumHexLen)
	storeIndexMeta(t, repo.Dir, active, "")
	storeIndexMeta(t, holdDir(t, repo, hold), hold, "")
	storeIndexMeta(t, archiveDir(t, repo, archive), archive, "")

	if err := rebuildIndexes(repo); err != nil {
		t.Fatal(err)
	}
	wantSHA := indexLine(hold, dirHold+"/"+hold) +
		indexLine(archive, dirArchives+"/"+archive) +
		indexLine(active, indexRelActive)
	if got := readLeafFile(t, repo, fileSHA256Index); got != wantSHA {
		t.Fatalf("sha256.index =\n%s\nwant\n%s", got, wantSHA)
	}
	if got := readLeafFile(t, repo, fileContentSHA256Index); got != "" {
		t.Fatalf("content index = %q, want empty", got)
	}
	assertNoIndexTemps(t, repo)
}

func TestLookupSHA256ReturnsRelativePath(t *testing.T) {
	repo := indexLeaf(t)
	active := strings.Repeat("a", sumHexLen)
	hold := strings.Repeat("b", sumHexLen)
	storeIndexMeta(t, repo.Dir, active, "")
	storeIndexMeta(t, holdDir(t, repo, hold), hold, "")
	if err := rebuildIndexes(repo); err != nil {
		t.Fatal(err)
	}
	// A correct index is only read. A rebuild would need to create a
	// temp file, which a read-only leaf directory refuses.
	if err := os.Chmod(repo.Dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(repo.Dir, 0o755)
	})

	got, err := lookupSHA256(repo, active)
	if err != nil {
		t.Fatal(err)
	}
	if got != indexRelActive {
		t.Fatalf("active rel = %s", got)
	}
	got, err = lookupSHA256(repo, hold)
	if err != nil {
		t.Fatal(err)
	}
	if got != dirHold+"/"+hold {
		t.Fatalf("hold rel = %s", got)
	}
}

func TestLookupRebuildsWhenPathMissing(t *testing.T) {
	repo := indexLeaf(t)
	active := strings.Repeat("a", sumHexLen)
	hold := strings.Repeat("b", sumHexLen)
	storeIndexMeta(t, repo.Dir, active, "")
	holdPath := holdDir(t, repo, hold)
	storeIndexMeta(t, holdPath, hold, "")
	if err := rebuildIndexes(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(holdPath); err != nil {
		t.Fatal(err)
	}

	_, err := lookupSHA256(repo, hold)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing path err = %v", err)
	}
	want := indexLine(active, indexRelActive)
	if got := readLeafFile(t, repo, fileSHA256Index); got != want {
		t.Fatalf("index after rebuild =\n%s\nwant\n%s", got, want)
	}
	got, err := lookupSHA256(repo, active)
	if err != nil || got != indexRelActive {
		t.Fatalf("active after rebuild = %s, %v", got, err)
	}
}

func TestLookupRebuildsWhenMetaMismatches(t *testing.T) {
	repo := indexLeaf(t)
	stale := strings.Repeat("a", sumHexLen)
	fresh := strings.Repeat("b", sumHexLen)
	storeIndexMeta(t, repo.Dir, stale, "")
	if err := rebuildIndexes(repo); err != nil {
		t.Fatal(err)
	}
	storeIndexMeta(t, repo.Dir, fresh, "")

	_, err := lookupSHA256(repo, stale)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("mismatched meta err = %v", err)
	}
	got, err := lookupSHA256(repo, fresh)
	if err != nil || got != indexRelActive {
		t.Fatalf("fresh lookup = %s, %v", got, err)
	}
	want := indexLine(fresh, indexRelActive)
	if body := readLeafFile(t, repo, fileSHA256Index); body != want {
		t.Fatalf("index after mismatch =\n%s\nwant\n%s", body, want)
	}
}

func TestContentIndexListsOnlyNonEmptyHashes(t *testing.T) {
	repo := indexLeaf(t)
	active := strings.Repeat("a", sumHexLen)
	hold := strings.Repeat("b", sumHexLen)
	archive := strings.Repeat("c", sumHexLen)
	content := strings.Repeat("d", sumHexLen)
	storeIndexMeta(t, repo.Dir, active, "")
	storeIndexMeta(t, holdDir(t, repo, hold), hold, content)
	storeIndexMeta(t, archiveDir(t, repo, archive), archive, "")
	if err := rebuildIndexes(repo); err != nil {
		t.Fatal(err)
	}

	want := indexLine(content, dirHold+"/"+hold)
	if got := readLeafFile(t, repo, fileContentSHA256Index); got != want {
		t.Fatalf("content index =\n%s\nwant\n%s", got, want)
	}
	shaBody := readLeafFile(t, repo, fileSHA256Index)
	if strings.Contains(shaBody, content) {
		t.Fatalf("payload index listed content hash:\n%s", shaBody)
	}
	got, err := lookupContentSHA256(repo, content)
	if err != nil || got != dirHold+"/"+hold {
		t.Fatalf("content lookup = %s, %v", got, err)
	}
	_, err = lookupContentSHA256(repo, active)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("empty content err = %v", err)
	}
}

func TestLookupIDPrefixUniqueOrAmbiguous(t *testing.T) {
	repo := indexLeaf(t)
	left := "abc111" + strings.Repeat("0", sumHexLen-6)
	right := "abc222" + strings.Repeat("0", sumHexLen-6)
	storeIndexMeta(t, repo.Dir, left, "")
	storeIndexMeta(t, holdDir(t, repo, right), right, "")
	if err := rebuildIndexes(repo); err != nil {
		t.Fatal(err)
	}

	got, err := lookupID(repo, "abc1")
	if err != nil || got != indexRelActive {
		t.Fatalf("unique prefix = %s, %v", got, err)
	}
	got, err = lookupID(repo, right)
	if err != nil || got != dirHold+"/"+right {
		t.Fatalf("full id = %s, %v", got, err)
	}
	// Fresh ambiguity is an error. Rebuilding would need a writable leaf.
	if err := os.Chmod(repo.Dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(repo.Dir, 0o755)
	})
	_, err = lookupID(repo, "abc")
	if err == nil || !strings.Contains(err.Error(), `ambiguous id reference "abc"`) {
		t.Fatalf("ambiguous err = %v", err)
	}
	if err := os.Chmod(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = lookupID(repo, "fff")
	if err == nil || !strings.Contains(err.Error(), `unknown id reference "fff"`) {
		t.Fatalf("unknown err = %v", err)
	}
}

func TestRebuildIndexesRecordsLegacyHoldPath(t *testing.T) {
	repo := indexLeaf(t)
	full := "0123456789abcdef" + strings.Repeat("a", sumHexLen-idHexLen)
	legacy := full[:idHexLen]
	dir, err := repo.HoldPath(legacy)
	if err != nil {
		t.Fatal(err)
	}
	storeIndexMeta(t, dir, full, "")
	if err := rebuildIndexes(repo); err != nil {
		t.Fatal(err)
	}
	want := indexLine(full, dirHold+"/"+legacy)
	if got := readLeafFile(t, repo, fileSHA256Index); got != want {
		t.Fatalf("legacy index =\n%s\nwant\n%s", got, want)
	}
	got, err := lookupID(repo, legacy)
	if err != nil || got != dirHold+"/"+legacy {
		t.Fatalf("legacy prefix = %s, %v", got, err)
	}
}

func TestRebuildIndexesSkipsUnreadableHold(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 000 directories")
	}
	repo := indexLeaf(t)
	active := strings.Repeat("a", sumHexLen)
	hold := strings.Repeat("b", sumHexLen)
	storeIndexMeta(t, repo.Dir, active, "")
	holdPath := holdDir(t, repo, hold)
	storeIndexMeta(t, holdPath, hold, "")
	if err := os.Chmod(holdPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(holdPath, 0o755)
	})
	if _, err := os.ReadFile(filepath.Join(holdPath, fileMeta)); err == nil {
		t.Skip("mode 000 directory is still readable")
	}

	if err := rebuildIndexes(repo); err != nil {
		t.Fatal(err)
	}
	want := indexLine(active, indexRelActive)
	if got := readLeafFile(t, repo, fileSHA256Index); got != want {
		t.Fatalf("index =\n%s\nwant\n%s", got, want)
	}
}

func TestEnsureIndexesCreatesMissingAndKeepsPresent(t *testing.T) {
	repo := indexLeaf(t)
	active := strings.Repeat("a", sumHexLen)
	storeIndexMeta(t, repo.Dir, active, "")
	if err := os.WriteFile(filepath.Join(repo.Dir, fileSHA256Index), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureIndexes(repo); err != nil {
		t.Fatal(err)
	}
	got, err := lookupSHA256(repo, active)
	if err != nil || got != indexRelActive {
		t.Fatalf("ensure missing content index = %s, %v", got, err)
	}

	const kept = "kept\n"
	for _, name := range []string{fileSHA256Index, fileContentSHA256Index} {
		if err := os.WriteFile(filepath.Join(repo.Dir, name), []byte(kept), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureIndexes(repo); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{fileSHA256Index, fileContentSHA256Index} {
		if got := readLeafFile(t, repo, name); got != kept {
			t.Fatalf("%s = %q, want kept", name, got)
		}
	}
}

func TestLookupRebuildsMalformedIndex(t *testing.T) {
	repo := indexLeaf(t)
	active := strings.Repeat("a", sumHexLen)
	storeIndexMeta(t, repo.Dir, active, "")
	if err := rebuildIndexes(repo); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo.Dir, fileSHA256Index)
	if err := os.WriteFile(path, []byte("not an index\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := lookupSHA256(repo, active)
	if err != nil || got != indexRelActive {
		t.Fatalf("malformed lookup = %s, %v", got, err)
	}
	want := indexLine(active, indexRelActive)
	if body := readLeafFile(t, repo, fileSHA256Index); body != want {
		t.Fatalf("rebuilt =\n%s\nwant\n%s", body, want)
	}
}

func indexLeaf(t *testing.T) Repo {
	t.Helper()
	repo, err := OpenRepo(t.TempDir(), "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

func storeIndexMeta(t *testing.T, dir, sum, content string) {
	t.Helper()
	meta := validMeta()
	meta.ID = sum
	meta.SHA256 = sum
	meta.ContentSHA256 = content
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Store(filepath.Join(dir, fileMeta), meta); err != nil {
		t.Fatal(err)
	}
}

func holdDir(t *testing.T, repo Repo, id string) string {
	t.Helper()
	dir, err := repo.HoldPath(id)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func archiveDir(t *testing.T, repo Repo, id string) string {
	t.Helper()
	dir, err := repo.ArchivePath(id)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func indexLine(key, rel string) string {
	return key + " " + rel + "\n"
}

func readLeafFile(t *testing.T, repo Repo, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo.Dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertNoIndexTemps(t *testing.T, repo Repo) {
	t.Helper()
	entries, err := os.ReadDir(repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, tempPrefix(fileSHA256Index)) ||
			strings.HasPrefix(name, tempPrefix(fileContentSHA256Index)) {
			t.Errorf("index temp left behind: %s", name)
		}
	}
}

func TestLookupRejectsBadSum(t *testing.T) {
	repo := indexLeaf(t)
	_, err := lookupSHA256(repo, "abcd")
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("sha256 err = %v", err)
	}
	_, err = lookupID(repo, "ABC")
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("id ref err = %v", err)
	}
}
