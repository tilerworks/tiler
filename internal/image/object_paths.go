package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const dirObjects = "_objects"

// ObjectDirPath locates an immutable version's payload directory.
func (repo Repo) ObjectDirPath(id string) (string, error) {
	return repo.objectPath(id, "")
}

// ObjectPayloadPath locates a single container or a split concat descriptor.
func (repo Repo) ObjectPayloadPath(node ImageMeta) (string, error) {
	switch node.Format.Type {
	case FormatSquashfs, FormatErofs, FormatDMG:
	default:
		return "", invalidPath(node.Format.Type)
	}
	name := activeImageName(node.Format.Type)
	if len(node.Blocks) > 0 {
		name = fileConcatView
	}
	return repo.objectPath(node.ID, name)
}

// ObjectBlockPath locates one validated block within an immutable version.
func (repo Repo) ObjectBlockPath(id, name string) (string, error) {
	if !isBlockName(name) {
		return "", invalidPath(name)
	}
	return repo.objectPath(id, name)
}

func (repo Repo) objectPath(id, name string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	if strings.TrimSpace(repo.Dir) == "" {
		return "", invalidPath(repo.Dir)
	}
	result := filepath.Join(repo.Dir, dirObjects, id)
	if name != "" {
		result = filepath.Join(result, name)
	}
	if err := repo.checkObjectComponents(result, name == ""); err != nil {
		return "", err
	}
	return result, nil
}

// checkObjectComponents inspects existing components within the library root.
// Callers must coordinate subsequent I/O; this is not a race-free open.
func (repo Repo) checkObjectComponents(target string, directory bool) error {
	root := filepath.Clean(repo.Dir)
	for range repo.Segments {
		root = filepath.Dir(root)
	}
	for current := target; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || ((current != target || directory) && !info.IsDir())) {
			return fmt.Errorf("%w: unsafe object component %q", ErrInvalidPath, current)
		}
		if current == root {
			return nil
		}
	}
}
