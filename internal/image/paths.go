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
	// Squashfs and erofs keep this name.
	ImageFile = "image.sqsh"
	// ImageDMG is the active full image name when meta format type
	// is dmg. It does not replace ImageFile.
	ImageDMG = "image.dmg"
	// IncrDir is the subdirectory holding incremental layer files.
	IncrDir = "_incr"
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
	dirBlocks      = "_blocks"
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
	// blocksTempFinal is the final name the tempName pattern consumes
	// for scratch blocks payload directories.
	blocksTempFinal = dirBlocks + tempPayloadSuffix
)

// ErrInvalidPath is a library-relative path that is absolute or
// contains an empty, ".", or ".." segment.
var ErrInvalidPath = errors.New("invalid library path")

// Repo is one named leaf directory under the image library.
type Repo struct {
	Segments []string
	Dir      string
	// LogsDir is the activity log root. An empty value fails an
	// activity open instead of writing beside the object.
	LogsDir string
}

// Name returns the canonical slash-joined library name.
func (repo Repo) Name() string {
	return strings.Join(repo.Segments, "/")
}

// OpenRepo parses name and joins every segment under imagesDir. It
// does not check leaf nesting; publish callers do that separately.
func OpenRepo(imagesDir, name string) (Repo, error) {
	segments, err := ParseName(name)
	if err != nil {
		return Repo{}, err
	}
	if strings.TrimSpace(imagesDir) == "" {
		return Repo{}, fmt.Errorf("%w: empty images dir", ErrInvalidPath)
	}
	parts := append([]string{imagesDir}, segments...)
	return Repo{
		Segments: segments,
		Dir:      filepath.Join(parts...),
	}, nil
}

// openLoggedRepo opens name and records the activity log root. An
// empty logsDir is kept: the activity open fails instead of falling
// back to the object directory.
func openLoggedRepo(imagesDir, logsDir, name string) (Repo, error) {
	repo, err := OpenRepo(imagesDir, name)
	if err != nil {
		return Repo{}, err
	}
	repo.LogsDir = logsDir
	return repo, nil
}

// ImagePath is the active squashfs file.
func (repo Repo) ImagePath() string {
	return filepath.Join(repo.Dir, fileImage)
}

// activeImageName is the single-file payload name for typ.
// dmg uses image.dmg. Squashfs, erofs, and any other type keep
// image.sqsh.
func activeImageName(typ string) string {
	if typ == FormatDMG {
		return ImageDMG
	}
	return fileImage
}

// MetaPath is the active metadata file.
func (repo Repo) MetaPath() string {
	return filepath.Join(repo.Dir, fileMeta)
}

// IncrPath is _incr/<id>.sqsh inside this repository.
func (repo Repo) IncrPath(id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	return filepath.Join(repo.Dir, dirIncr, id+squashExt), nil
}

// BlocksDirPath is _blocks/<full-id>, the directory holding the byte
// blocks of one split payload. New block trees always use a full id.
func (repo Repo) BlocksDirPath(id string) (string, error) {
	return repo.idSubdir(dirBlocks, id)
}

// BlockPath is _blocks/<full-id>/<name> for one manifest block. The
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

// tempBlocksDirName is a scratch payload directory name for the
// blocks form. It follows the tempName pattern, so repair recognizes
// and sweeps a leftover after a crashed publish.
func tempBlocksDirName() (string, error) {
	return tempName(blocksTempFinal)
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
	if checkID(id) == nil {
		return filepath.Join(repo.Dir, kind, id), nil
	}
	return "", invalidPath(id)
}

// checkID accepts a stored id: 64 lowercase hex digits, the payload
// sha256. Callers that still hold a user prefix resolve it first.
// New tiler-written directory names must pass checkID; legacy 16-hex
// on-disk names use onDiskID / legacyID instead.
func checkID(id string) error {
	if !isLowerHex(id, sumHexLen) {
		return invalidPath(id)
	}
	return checkRel(id)
}

func onDiskID(id string) bool {
	return checkID(id) == nil || isLowerHex(id, idHexLen)
}

// resolveID returns the one candidate that has ref as a prefix.
// ref is lowercase hex of length 1 to 64. Zero matches and more
// than one match are errors, and both quote ref.
func resolveID(candidates []string, ref string) (string, error) {
	if !isIDRef(ref) {
		return "", invalidPath(ref)
	}
	var match string
	for _, candidate := range candidates {
		if !strings.HasPrefix(candidate, ref) {
			continue
		}
		if match != "" {
			return "", fmt.Errorf("ambiguous id reference %q", ref)
		}
		match = candidate
	}
	if match == "" {
		return "", fmt.Errorf("unknown id reference %q", ref)
	}
	return match, nil
}

// IsIDRef reports a lowercase hex id reference of length 1 to 64.
func IsIDRef(ref string) bool {
	return isIDRef(ref)
}

func isIDRef(ref string) bool {
	if len(ref) < 1 || len(ref) > sumHexLen {
		return false
	}
	return isLowerHex(ref, len(ref))
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
