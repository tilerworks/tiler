package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnknownDigestSkipsPayloadReadAndRejectsStaleSources(t *testing.T) {
	dir := t.TempDir()
	body := []byte("external-payload-bytes")
	payload := filepath.Join(dir, "payload.sqsh")
	if err := os.WriteFile(payload, body, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(payload)
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(dir, "location")
	if err := writePathRef(payload, leaf); err != nil {
		t.Fatal(err)
	}
	node := ImageMeta{ID: UnknownSHA256, SHA256: UnknownSHA256, Size: info.Size(), MtimeUnix: info.ModTime().Unix()}
	proveUnreadWithMode000(t, payload)
	skipped, err := VerifyMountDigest(node, leaf, true)
	if err != nil || !skipped {
		t.Fatalf("verify unknown = %v, skipped %v", err, skipped)
	}

	knownBody := []byte("known-member")
	knownPath := filepath.Join(dir, "known.sqsh")
	if err := os.WriteFile(knownPath, knownBody, 0o644); err != nil {
		t.Fatal(err)
	}
	kinfo, err := os.Stat(knownPath)
	if err != nil {
		t.Fatal(err)
	}
	known := ImageMeta{Size: kinfo.Size(), MtimeUnix: kinfo.ModTime().Unix(), SHA256: digestHex([]byte("other"))}
	if _, err := VerifyMountDigest(known, knownPath, true); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("known mismatch = %v", err)
	}
	known.SHA256 = digestHex(knownBody)
	if skipped, err = VerifyMountDigest(known, knownPath, true); err != nil || skipped {
		t.Fatalf("known digest = %v, skipped %v", err, skipped)
	}
	blocks := Target{Form: PayloadBlocks, Blocks: []BlockFile{{
		Path: knownPath, Name: "blk.000", Size: known.Size,
		MtimeUnix: known.MtimeUnix, SHA256: digestHex([]byte("other")),
	}}}
	if err := VerifyTarget(blocks); err == nil || !strings.Contains(err.Error(), "blk.000") {
		t.Fatalf("known block = %v", err)
	}

	if err := os.Chmod(payload, 0o644); err != nil {
		t.Fatal(err)
	}
	when := info.ModTime().Add(time.Hour)
	if err := os.Chtimes(payload, when, when); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyMountDigest(node, leaf, true); err == nil || !strings.Contains(err.Error(), "mtime") {
		t.Fatalf("changed mtime = %v", err)
	}
	if err := os.WriteFile(payload, append(body, 'x'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyMountDigest(node, leaf, false); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("changed size = %v", err)
	}
	if err := os.Remove(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyMountDigest(node, leaf, true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing path-ref target = %v", err)
	}
}

func TestUnknownDigestCacheIsNotShared(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	alpha := filepath.Join(dir, "alpha.sqsh")
	beta := filepath.Join(dir, "beta.sqsh")
	if err := os.WriteFile(alpha, []byte("alpha-source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(beta, []byte("beta-source-b"), 0o644); err != nil {
		t.Fatal(err)
	}
	leafA, leafB := filepath.Join(dir, "alpha-loc"), filepath.Join(dir, "beta-loc")
	if err := writePathRef(alpha, leafA); err != nil {
		t.Fatal(err)
	}
	if err := writePathRef(beta, leafB); err != nil {
		t.Fatal(err)
	}
	proveUnreadWithMode000(t, alpha, beta)
	gotA, sumA, err := EnsureCached(cache, leafA, UnknownSHA256)
	gotB, sumB, errB := EnsureCached(cache, leafB, UnknownSHA256)
	if err != nil || errB != nil || gotA != alpha || gotB != beta || gotA == gotB ||
		sumA != UnknownSHA256 || sumB != UnknownSHA256 {
		t.Fatalf("bypass = %s %s %v, %s %s %v", gotA, sumA, err, gotB, sumB, errB)
	}
	if _, statErr := os.Stat(filepath.Join(cache, UnknownSHA256+SquashExt)); !os.IsNotExist(statErr) {
		t.Fatalf("zero-key cache: %v", statErr)
	}
	gone := filepath.Join(dir, "gone.sqsh")
	if err := os.WriteFile(gone, []byte("removed-target"), 0o644); err != nil {
		t.Fatal(err)
	}
	goneLeaf := filepath.Join(dir, "gone-loc")
	if err := writePathRef(gone, goneLeaf); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsureCached(cache, goneLeaf, UnknownSHA256); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling path-ref = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(cache, UnknownSHA256+SquashExt)); !os.IsNotExist(statErr) {
		t.Fatalf("zero-key cache: %v", statErr)
	}
	if _, err := AssembleBlocks(cache, nil, UnknownSHA256, 1); err == nil {
		t.Fatal("assembled a zero-key cache")
	}
	assertDirEmpty(t, cache)
	if err := os.Chmod(alpha, 0o644); err != nil {
		t.Fatal(err)
	}
	want := digestHex([]byte("alpha-source"))
	path, sum, err := EnsureCached(cache, leafA, want)
	if err != nil || sum != want || path == alpha {
		t.Fatalf("known cache = %s %s %v", path, sum, err)
	}
}

func TestUnknownDigestIndexesAndSelectorsStayScoped(t *testing.T) {
	repo := testRepo(t)
	manifest := versionFixture()
	manifest.Name = repo.Name()
	manifest.Images[0].ContentSHA256 = strings.Repeat("c", 64)
	zero := manifest.Images[0]
	zero.ID, zero.SHA256, zero.ContentSHA256 = UnknownSHA256, UnknownSHA256, UnknownSHA256
	zero.Parent, zero.Kind, zero.Size, zero.Description = "", KindFull, 7, ""
	manifest.Images = append(manifest.Images, zero)
	if _, err := EncodeVersionManifest(manifest); err != nil {
		t.Fatal(err)
	}
	storeVersionIndexFixture(t, repo, manifest)
	if _, err := EnsureVersionIndexes(repo); err != nil {
		t.Fatal(err)
	}
	payload := readLeafFile(t, repo, fileSHA256Index)
	content := readLeafFile(t, repo, fileContentSHA256Index)
	if strings.Contains(payload, UnknownSHA256) || strings.Contains(content, UnknownSHA256) {
		t.Fatalf("zero key indexed\n%s\n%s", payload, content)
	}
	if !strings.Contains(payload, manifest.Images[0].ID) || !strings.Contains(content, strings.Repeat("c", 64)) {
		t.Fatal("known digest dropped from the index")
	}
	stack, err := manifest.SelectVersion(manifest.Name + ":" + UnknownSHA256[:4])
	if err != nil || len(stack) != 1 || stack[0].ID != UnknownSHA256 || stack[0].Size != 7 {
		t.Fatalf("zero selector = %+v %v", stack, err)
	}
	known, err := manifest.SelectVersion(manifest.Name + ":" + manifest.Images[0].ID)
	if err != nil || known[0].ID != manifest.Images[0].ID {
		t.Fatalf("known selector = %+v %v", known, err)
	}
	otherRepo, err := OpenRepo(t.TempDir(), "vendor/app")
	if err != nil {
		t.Fatal(err)
	}
	other := VersionManifest{Version: VersionManifestVersion, Name: otherRepo.Name(), Images: []ImageMeta{zero}}
	other.Images[0].Size = 9
	storeVersionIndexFixture(t, otherRepo, other)
	if _, err := EnsureVersionIndexes(otherRepo); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(otherRepo.Dir, fileSHA256Index), nil)
	if _, err := manifest.SelectVersion(other.Name + ":" + UnknownSHA256[:8]); err == nil {
		t.Fatal("selector crossed image names")
	}
	otherStack, err := other.SelectVersion(other.Name + ":" + UnknownSHA256)
	if err != nil || otherStack[0].Size != 9 || otherStack[0].ID != stack[0].ID {
		t.Fatalf("other name = %+v %v", otherStack, err)
	}
}

// proveUnreadWithMode000 chmods each path to 0 when this process
// cannot open such a file. Root can open mode 000, which does not
// prove a hash was skipped, so the mode is restored and callers must
// not treat a later success as unreadability evidence.
func proveUnreadWithMode000(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		_ = file.Close()
		for _, restore := range paths {
			if err := os.Chmod(restore, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	t.Cleanup(func() {
		for _, path := range paths {
			_ = os.Chmod(path, 0o644)
		}
	})
}
