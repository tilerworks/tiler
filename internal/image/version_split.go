package image

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func publishSplitVersion(repo Repo, staged string, node ImageMeta, descriptionSet bool, seam hooks) (ImageMeta, error) {
	if _, err := verifySplitVersion(staged, node); err != nil {
		return ImageMeta{}, err
	}
	dir, err := repo.ObjectDirPath(node.ID)
	if err != nil {
		return ImageMeta{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ImageMeta{}, err
	}
	for _, name := range splitVersionNames(node) {
		dst, err := repo.objectPath(node.ID, name)
		if err != nil {
			return ImageMeta{}, err
		}
		if err := copyVersionFile(filepath.Join(staged, name), dst); err != nil {
			return ImageMeta{}, err
		}
	}
	if node, err = verifySplitVersion(dir, node); err != nil {
		return ImageMeta{}, err
	}
	for range len(repo.Segments) + 3 {
		if err := syncDir(dir, seam); err != nil {
			return ImageMeta{}, err
		}
		dir = filepath.Dir(dir)
	}
	return appendVersion(repo, node, descriptionSet, seam)
}

func splitVersionNames(node ImageMeta) []string {
	names := make([]string, 0, len(node.Blocks)+1)
	for _, block := range node.Blocks {
		names = append(names, block.Name)
	}
	return append(names, fileConcatView)
}

func verifySplitVersion(dir string, node ImageMeta) (ImageMeta, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return ImageMeta{}, err
	}
	if !info.IsDir() {
		return ImageMeta{}, fmt.Errorf("version publish: split directory required: %s", dir)
	}
	node.Blocks = slices.Clone(node.Blocks)
	whole, size := sha256.New(), int64(0)
	for i, block := range node.Blocks {
		path := filepath.Join(dir, block.Name)
		info, err := verifyVersionFile(path, ImageMeta{Size: block.Size, SHA256: block.SHA256})
		if err != nil {
			return ImageMeta{}, err
		}
		file, err := os.Open(path)
		if err != nil {
			return ImageMeta{}, err
		}
		n, copyErr := io.Copy(whole, file)
		if err := errors.Join(copyErr, file.Close()); err != nil {
			return ImageMeta{}, err
		}
		size += n
		node.Blocks[i].MtimeUnix = info.ModTime().Unix()
	}
	if size != node.Size || fmt.Sprintf("%x", whole.Sum(nil)) != node.SHA256 {
		return ImageMeta{}, errors.New("version publish: logical split payload mismatch")
	}
	names := splitVersionNames(node)
	view := []byte(strings.Join(names[:len(names)-1], "\n") + "\n")
	_, err = verifyVersionFile(filepath.Join(dir, fileConcatView), ImageMeta{Size: int64(len(view)), SHA256: ID(sha256.Sum256(view))})
	return node, err
}
