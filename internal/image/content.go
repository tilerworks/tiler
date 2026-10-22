package image

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// dirContentSHA256 folds every relative path, node kind, symlink
// target, and file content under root into one lowercase hex digest.
// Directory entries contribute their path only. File mtimes are not
// included, so a touch without a content change stays the same digest.
func dirContentSHA256(root string) (string, error) {
	var lines []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		line, lineErr := contentEntry(path, filepath.ToSlash(rel), entry)
		if lineErr != nil {
			return lineErr
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

func contentEntry(path, rel string, entry fs.DirEntry) (string, error) {
	switch {
	case entry.IsDir():
		return rel + "\tdir", nil
	case entry.Type()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		return rel + "\tlink\t" + target, nil
	case entry.Type()&os.ModeNamedPipe != 0 ||
		entry.Type()&os.ModeSocket != 0 ||
		entry.Type()&os.ModeDevice != 0:
		return "", fmt.Errorf("content: unsupported node %s", rel)
	default:
		sum, err := fileSHA256(path)
		if err != nil {
			return "", err
		}
		return rel + "\tfile\t" + hex.EncodeToString(sum[:]), nil
	}
}

// dmgContentIdle reports an active dmg that already records the same
// logical content and compression, with no incremental chain.
func dmgContentIdle(active Meta, contentSHA256, compression string) bool {
	return active.Format.Type == FormatDMG &&
		active.Format.Compression == compression &&
		len(active.Chain) == 0 &&
		contentSHA256 != "" &&
		active.ContentSHA256 == contentSHA256
}
