package image

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

const (
	fileImage   = "image.sqsh"
	fileMeta    = "meta.yaml"
	dirIncr     = "incr"
	dirArchives = "archives"
	dirHold     = "hold"
	squashExt   = ".sqsh"
)

// ErrInvalidPath is a library-relative path that is absolute or
// contains an empty, ".", or ".." segment.
var ErrInvalidPath = errors.New("invalid library path")

// Repo is one owner/repo directory under the image library.
type Repo struct {
	Owner string
	Repo  string
	Dir   string
}

// OpenRepo parses name and joins it under imagesDir. The name must
// be an explicit owner/repo pair.
func OpenRepo(imagesDir, name string) (Repo, error) {
	owner, repo, err := ParseName(name)
	if err != nil {
		return Repo{}, err
	}
	if strings.TrimSpace(imagesDir) == "" {
		return Repo{}, fmt.Errorf("%w: empty images dir", ErrInvalidPath)
	}
	return Repo{
		Owner: owner,
		Repo:  repo,
		Dir:   filepath.Join(imagesDir, owner, repo),
	}, nil
}

// ImagePath is the active squashfs file.
func (repo Repo) ImagePath() string {
	return filepath.Join(repo.Dir, fileImage)
}

// MetaPath is the active metadata file.
func (repo Repo) MetaPath() string {
	return filepath.Join(repo.Dir, fileMeta)
}

// IncrPath is incr/<id>.sqsh inside this repository.
func (repo Repo) IncrPath(id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	return filepath.Join(repo.Dir, dirIncr, id+squashExt), nil
}

// ArchivePath is archives/<full-id> for a retired chain.
func (repo Repo) ArchivePath(id string) (string, error) {
	return repo.chainPath(dirArchives, id)
}

// HoldPath is hold/<full-id> for a chain waiting on a reference.
func (repo Repo) HoldPath(id string) (string, error) {
	return repo.chainPath(dirHold, id)
}

func (repo Repo) chainPath(kind, id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	return filepath.Join(repo.Dir, kind, id), nil
}

func checkID(id string) error {
	if !isLowerHex(id, idHexLen) {
		return invalidPath(id)
	}
	return checkRel(id)
}

func checkRel(rel string) error {
	if rel == "" || path.IsAbs(rel) || rel != path.Clean(rel) {
		return invalidPath(rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return invalidPath(rel)
		}
	}
	return nil
}

func invalidPath(rel string) error {
	return fmt.Errorf("%w: %q", ErrInvalidPath, rel)
}
