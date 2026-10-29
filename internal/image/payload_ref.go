package image

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/container"
)

// pathRefMaxSize caps a path-ref leaf. Larger regular files are treated
// as local payloads (or left for DetectFile), never as path text.
const pathRefMaxSize = 4096

// ResolvePayloadBytesPath maps a library leaf path to the file whose
// bytes are the container. A symlink or path-ref leaf resolves to the
// external absolute path. A path-ref whose target is missing returns
// that stat error instead of the location file. A real payload
// (DetectFile succeeds on the leaf, following a symlink when present)
// returns the leaf itself so callers keep opening the library path
// when the OS follows links.
func ResolvePayloadBytesPath(leaf string) (string, error) {
	info, err := os.Lstat(leaf)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(leaf)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			return "", fmt.Errorf("image: payload symlink %s is not absolute", leaf)
		}
		return target, nil
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("image: payload %s is not a regular file", leaf)
	}
	if isContainerPayload(leaf) {
		return leaf, nil
	}
	target, ok, err := readPathRefContent(leaf, info.Size())
	if err != nil {
		return "", err
	}
	if ok {
		return target, nil
	}
	return leaf, nil
}

// isContainerPayload reports whether leaf looks like a registered
// container. Symlinks are not passed here; the caller already branched.
func isContainerPayload(leaf string) bool {
	if _, err := container.DetectFile(leaf); err == nil {
		return true
	}
	ok, err := container.ReadKolyTrailer(leaf)
	return err == nil && ok
}

// readExternalLinkTarget reports the absolute external path when leaf
// is a symlink or path-ref. ok is false when the leaf is absent or a
// local payload (or unrecognized).
func readExternalLinkTarget(leaf string) (string, bool, error) {
	info, err := os.Lstat(leaf)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(leaf)
		if err != nil {
			return "", false, err
		}
		if !filepath.IsAbs(target) {
			return "", true, fmt.Errorf("image: payload symlink is not absolute")
		}
		return target, true, nil
	}
	if !info.Mode().IsRegular() {
		return "", false, nil
	}
	if isContainerPayload(leaf) {
		return "", false, nil
	}
	target, ok, err := readPathRefContent(leaf, info.Size())
	// Registration treats a missing target as "not a link".
	// Resolution returns the stat error so the location file is
	// not used as the payload.
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	return target, ok, err
}

// readPathRefContent parses a small regular file as a single-line
// absolute path. A grammar match whose target is missing returns
// that error. Any other body is not a path-ref.
func readPathRefContent(leaf string, size int64) (string, bool, error) {
	if size <= 0 || size > pathRefMaxSize {
		return "", false, nil
	}
	body, err := os.ReadFile(leaf)
	if err != nil {
		return "", false, err
	}
	if bytes.IndexByte(body, 0) >= 0 {
		return "", false, nil
	}
	line := strings.TrimSpace(string(body))
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return "", false, nil
	}
	if !filepath.IsAbs(line) {
		return "", false, nil
	}
	target := filepath.Clean(line)
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if err != nil || !info.Mode().IsRegular() {
		return "", false, nil
	}
	return target, true, nil
}

// writePathRef writes absSrc plus a trailing newline into path as a
// regular file (git core.symlinks=false style).
func writePathRef(absSrc, path string) error {
	tmp := path + ".tmp-pathref"
	if err := os.WriteFile(tmp, []byte(absSrc+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
