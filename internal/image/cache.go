package image

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// EnsureCached returns the local copy of src named by its content
// digest, as <cacheDir>/<sha256>.sqsh, together with that digest. An
// empty want computes the digest from src first, so a file-path image
// keys the cache by its own bytes; library members pass the digest
// recorded in metadata and never re-read the source for keying.
//
// A hit re-hashes the cached copy because local reads are cheap:
// matching bytes return without copying, and a corrupted copy is
// replaced. A miss copies through a temp file inside the cache
// directory, syncs it, checks the copy against the digest, and only
// then renames it into place, so a source that disagrees with the
// recorded digest fails before anything is cached and no partial
// entry survives a failure.
func EnsureCached(cacheDir, src, want string) (path string, sha256 string, err error) {
	sum := want
	if sum == "" {
		digest, digestErr := fileSHA256(src)
		if digestErr != nil {
			return "", "", fmt.Errorf("cache: %w", digestErr)
		}
		sum = hexDigest(digest)
	} else if !isLowerHex(sum, sumHexLen) {
		// The digest names the cache file, so refuse anything that
		// could escape the cache directory.
		return "", "", fmt.Errorf("cache: invalid sha256 %q", sum)
	}
	path = filepath.Join(cacheDir, sum+SquashExt)
	if cachedOK(path, sum) {
		return path, sum, nil
	}
	if err := copyVerified(cacheDir, src, sum, path); err != nil {
		return "", "", err
	}
	return path, sum, nil
}

// cachedOK reports whether path already holds exactly the bytes the
// digest names. A hit still verifies the whole copy to catch a
// corrupted cache.
func cachedOK(path, sum string) bool {
	digest, err := fileSHA256(path)
	if err != nil {
		return false
	}
	return hexDigest(digest) == sum
}

// copyVerified copies src into a temp file inside cacheDir, checks
// the copy against sum, and renames it onto the final path. Any
// failure removes the temp file and leaves no cache entry behind.
func copyVerified(cacheDir, src, sum, path string) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	tmp, err := os.CreateTemp(cacheDir, ".tmp-"+sum+"-*")
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	defer removeFile(tmp.Name())
	if err := copySynced(tmp, src); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	digest, err := fileSHA256(tmp.Name())
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if got := hexDigest(digest); got != sum {
		return fmt.Errorf("cache: %s does not match sha256 %s", src, sum)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	return nil
}

// copySynced streams src into the open temp file and syncs it, so the
// rename only ever publishes complete bytes. Large images never pass
// through memory twice.
func copySynced(tmp *os.File, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(tmp, in)
	closeErr := in.Close()
	if err := joinWriteErr(copyErr, closeErr); err != nil {
		return err
	}
	syncErr := tmp.Sync()
	closeTmp := tmp.Close()
	return joinWriteErr(syncErr, closeTmp)
}
