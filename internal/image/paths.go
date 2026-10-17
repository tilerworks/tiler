package image

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// Layout names shared with packages that match library paths.
const (
	// ImageFile is the active full image name in a repository dir.
	ImageFile = "image.sqsh"
	// IncrDir is the subdirectory holding incremental layer files.
	IncrDir = "incr"
	// SquashExt is the file extension of every packed image file.
	SquashExt = ".sqsh"
)

const (
	fileImage = ImageFile
	fileMeta  = "meta.yaml"
	// fileConcatView is the concat description file inside a blocks
	// payload directory. The marker in its name lets the optional
	// concat FUSE tool recognize it as a concat description.
	fileConcatView = fileImage + "-concat-view"
	dirIncr        = IncrDir
	dirArchives    = "archives"
	dirHold        = "hold"
	dirBlocks      = "blocks"
	dirMembers     = "members"
	squashExt      = SquashExt

	// blockNamePrefix and blockNameFmt name the byte blocks of one
	// split payload; the three-digit format bounds the manifest.
	blockNamePrefix = "blk."
	blockNameFmt    = blockNamePrefix + "%03d"
	// maxBlocks is the manifest capacity of one blocks payload.
	maxBlocks = 999
	// tempPayloadSuffix marks a scratch payload directory that a
	// publish renames into place; repair sweeps leftovers by the
	// same prefix.
	tempPayloadSuffix = ".tmp"
	// blocksTempFinal and membersTempFinal are the final names the
	// tempName pattern consumes for scratch payload directories.
	blocksTempFinal  = dirBlocks + tempPayloadSuffix
	membersTempFinal = dirMembers + tempPayloadSuffix
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
	return repo.idSubdir(dirArchives, id)
}

// HoldPath is hold/<full-id> for a chain waiting on a reference.
func (repo Repo) HoldPath(id string) (string, error) {
	return repo.idSubdir(dirHold, id)
}

// BlocksDirPath is blocks/<full-id>, the directory holding the byte
// blocks of one split payload.
func (repo Repo) BlocksDirPath(id string) (string, error) {
	return repo.idSubdir(dirBlocks, id)
}

// MembersDirPath is the members/ directory holding the directory
// partitions of one split payload.
func (repo Repo) MembersDirPath() string {
	return filepath.Join(repo.Dir, dirMembers)
}

// BlockPath is blocks/<full-id>/<name> for one manifest block. The
// name must be the blk.NNN form, which also rules out path escapes.
func (repo Repo) BlockPath(id, name string) (string, error) {
	dir, err := repo.BlocksDirPath(id)
	if err != nil {
		return "", err
	}
	if !isBlockName(name) {
		return "", invalidPath(name)
	}
	return filepath.Join(dir, name), nil
}

// MemberPath is members/<name>.sqsh for one manifest member.
func (repo Repo) MemberPath(name string) (string, error) {
	if err := checkSegment(name); err != nil {
		return "", invalidPath(name)
	}
	return filepath.Join(repo.Dir, memberImageName(name)), nil
}

// tempBlocksDirName is a scratch payload directory name for the
// blocks form. It follows the tempName pattern, so repair recognizes
// and sweeps a leftover after a crashed publish.
func tempBlocksDirName() (string, error) {
	return tempName(blocksTempFinal)
}

// tempMembersDirName is a scratch payload directory name for the
// members form, following the same tempName pattern.
func tempMembersDirName() (string, error) {
	return tempName(membersTempFinal)
}

// memberImageName is the library-relative image path recorded for one
// manifest member.
func memberImageName(name string) string {
	return dirMembers + "/" + name + squashExt
}

// isBlockName reports whether name is blk.NNN with exactly three
// digits.
func isBlockName(name string) bool {
	digits, ok := strings.CutPrefix(name, blockNamePrefix)
	if !ok || len(digits) != 3 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (repo Repo) idSubdir(kind, id string) (string, error) {
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
