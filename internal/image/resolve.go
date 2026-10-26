package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
)

// ErrUnknownImage means a library reference whose active slot holds
// no image. Command layers treat it as a usage problem.
var ErrUnknownImage = errors.New("unknown image")

// ErrNotImageFile means a file-path argument that does not name an
// existing regular file. Command layers treat it as a usage problem.
var ErrNotImageFile = errors.New("not an image file")

// Target is one resolved mount subject: a library reference expanded
// into its active stack or split payload manifest, or a single
// file-path image.
type Target struct {
	// Library is true for a library-name reference and false for a
	// file path.
	Library bool
	// Name is the canonical library name when Library is true.
	Name string
	// ID is the full image id of the resolved active chain or split
	// payload when Library is true, and empty for a file path.
	ID string
	// Format is the container type recorded on library metadata,
	// for example "squashfs". A file path leaves it empty; the
	// mount command detects that file.
	Format string
	// Members lists the container files to stack-mount: the full
	// image first, then chain layers in stored oldest-first order.
	Members []Member
	// Form names the split payload shape: empty for the single-file
	// cascade, or blocks for the byte-block form.
	Form string
	// Size and SHA256 are the logical whole-image facts of a blocks
	// payload: the reassembled byte digest and size.
	Size   int64
	SHA256 string
	// BlocksDir is the directory holding the byte blocks and the
	// concat description file of a blocks target.
	BlocksDir string
	// ConcatView is the absolute path of the concat description
	// file the optional zero-reassembly mount route consumes. Only
	// blocks targets carry one; the file itself may be absent from
	// a hand-crafted payload, which the mount route reports.
	ConcatView string
	// Blocks lists the byte-block files of a blocks target in
	// manifest order.
	Blocks []BlockFile
}

// BlockFile is one manifest byte block paired with its absolute
// path, so the mount layer can verify and reassemble without
// revisiting the repository layout.
type BlockFile struct {
	// Path is the absolute block file _blocks/<full-id>/<name>.
	Path string
	// Name is the manifest block name blk.NNN.
	Name string
	// Size is the recorded block size in bytes.
	Size int64
	// MtimeUnix is the recorded modification time.
	MtimeUnix int64
	// SHA256 is the recorded content digest.
	SHA256 string
}

// Member is one container file of the stack with the facts recorded
// in metadata, so callers can run the cheap quick check by default
// and a full sha256 verification on demand. A file-path member
// records zero facts because no metadata exists for it. For a
// blocks-form target, Path stays empty and Blocks holds the layer's
// byte-block files; mount reassembles each layer before stacking.
type Member struct {
	// Path is the full image file or an _incr/<id>.sqsh layer file.
	// Empty for a blocks-form layer until reassembly fills a cache path.
	Path string
	// ID is the full or incremental image id of a blocks-form layer.
	// Empty for the single-file cascade and for file-path mounts.
	ID string
	// Size is the recorded payload size in bytes.
	Size int64
	// MtimeUnix is the recorded modification time.
	MtimeUnix int64
	// SHA256 is the recorded content digest.
	SHA256 string
	// Blocks is the byte-block manifest of one blocks-form layer.
	// Empty for the single-file cascade.
	Blocks []BlockFile
}

// LocatePinned returns the absolute leaf root for a library name plus
// id reference: the active repository directory or _hold/<id>. It
// uses the same location rules as ResolvePinned (index, then HoldPath
// fallback for an on-disk id directory). Archives are refused.
func LocatePinned(imagesDir, name, idRef string) (string, error) {
	repo, err := OpenRepo(imagesDir, name)
	if err != nil {
		return "", pinError(name, idRef, err)
	}
	return pinnedRoot(repo, name, idRef)
}

// ResolvePinned expands a library image by name plus an id reference
// (full sha256 or a unique lowercase hex prefix). The leaf root comes
// from sha256.index: active (".") or _hold/<id>. Archives never
// remount. When the index has no match, HoldPath is a fallback only
// if idRef is itself an existing hold directory name (64-hex or a
// legacy 16-hex directory). Zero matches, ambiguous matches, and a
// missing hold fail and quote the name and id. Name-only ResolveMount
// stays active-only.
func ResolvePinned(cfg config.Config, name, idRef string) (Target, error) {
	root, err := LocatePinned(cfg.ImagesDir, name, idRef)
	if err != nil {
		return Target{}, err
	}
	meta, err := Load(filepath.Join(root, fileMeta))
	if err != nil {
		return Target{}, pinError(name, idRef, err)
	}
	if PayloadKind(meta) == PayloadBlocks {
		return resolveBlocksAt(root, name, meta)
	}
	return resolveStackAt(root, name, meta)
}

// pinnedRoot locates the absolute leaf root for a pin: index hit for
// active or hold, else HoldPath when idRef names an existing hold
// directory. An archive index hit is refused.
func pinnedRoot(repo Repo, name, idRef string) (string, error) {
	rel, err := lookupID(repo, idRef)
	switch {
	case err == nil:
		if isArchiveRel(rel) {
			return "", pinError(name, idRef, errors.New("archived image cannot be remounted"))
		}
		return indexRootPath(repo, rel), nil
	case isUnknownIDRef(err):
		return holdRootFallback(repo, name, idRef)
	default:
		return "", pinError(name, idRef, err)
	}
}

func holdRootFallback(repo Repo, name, idRef string) (string, error) {
	if !onDiskID(idRef) {
		return "", pinError(name, idRef, fmt.Errorf("unknown id reference %q", idRef))
	}
	hold, err := repo.HoldPath(idRef)
	if err != nil {
		return "", pinError(name, idRef, err)
	}
	info, err := os.Stat(hold)
	if err != nil {
		return "", pinError(name, idRef, err)
	}
	if !info.IsDir() {
		return "", pinError(name, idRef, fmt.Errorf("%s is not a directory", hold))
	}
	return hold, nil
}

func indexRootPath(repo Repo, rel string) string {
	if rel == indexRelActive {
		return repo.Dir
	}
	return filepath.Join(repo.Dir, filepath.FromSlash(rel))
}

func isArchiveRel(rel string) bool {
	return rel == dirArchives || strings.HasPrefix(rel, dirArchives+"/")
}

// IsUnknownIDRef reports a missing id lookup that quotes the
// reference. Callers that tried a name first can keep the name error.
func IsUnknownIDRef(err error) bool {
	return isUnknownIDRef(err)
}

func isUnknownIDRef(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unknown id reference")
}

func pinError(name, idRef string, err error) error {
	return fmt.Errorf("mount: %s id %s: %w", name, idRef, err)
}

// ResolveMount turns a mount argument into what a mount consumes:
// the ordered stack member list, or the manifest of a split payload.
// An existing regular file wins over a library name: filepath.Clean
// the argument, then os.Stat. A regular file uses the file resolver.
// os.ErrNotExist, or a path that exists but is not a regular file,
// falls through to ParseName and library resolution. Any other Stat
// error is a runtime error and is not treated as a library name.
// Relative paths are Stat'd without forcing Abs; resolveFile still
// absolutizes when opening. Library resolution reads only the active
// slot and follows the payload kind its metadata names; archives and
// delayed-archive holds never participate.
func ResolveMount(cfg config.Config, arg string) (Target, error) {
	cleaned := filepath.Clean(arg)
	info, err := os.Stat(cleaned)
	switch {
	case err == nil && info.Mode().IsRegular():
		return resolveFile(cleaned)
	case err == nil:
		// Exists but not a regular file — try a library name.
	case errors.Is(err, os.ErrNotExist):
		// Missing path — try a library name.
	default:
		return Target{}, fmt.Errorf("mount: %w", err)
	}
	if _, err := ParseName(arg); err == nil {
		return resolveLibrary(cfg, arg)
	}
	return resolveFile(arg)
}

// resolveLibrary expands the active metadata of one repository by
// payload kind: a single-file stack or a byte-block manifest.
func resolveLibrary(cfg config.Config, name string) (Target, error) {
	repo, err := OpenRepo(cfg.ImagesDir, name)
	if err != nil {
		return Target{}, fmt.Errorf("mount: %w", err)
	}
	meta, exists, err := loadActive(repo)
	if err != nil {
		return Target{}, fmt.Errorf("mount: %w", err)
	}
	if !exists {
		return Target{}, fmt.Errorf("mount: %w %s", ErrUnknownImage, name)
	}
	if PayloadKind(meta) == PayloadBlocks {
		return resolveBlocks(repo, name, meta)
	}
	return resolveStack(repo, name, meta)
}

// resolveStack expands the active full image plus its chain layers.
func resolveStack(repo Repo, name string, meta Meta) (Target, error) {
	return resolveStackAt(repo.Dir, name, meta)
}

// resolveStackAt expands the full image plus chain layers under root
// (the active leaf or a hold directory that preserves that layout).
func resolveStackAt(root, name string, meta Meta) (Target, error) {
	if depth := 1 + len(meta.Chain); depth > maxStackDepth {
		return Target{}, fmt.Errorf(
			"mount: %s stack holds %d members; the limit is %d",
			name, depth, maxStackDepth)
	}
	members, err := stackMembersAt(root, meta)
	if err != nil {
		return Target{}, err
	}
	return Target{
		Library: true, Name: name, ID: meta.ID,
		Format: meta.Format.Type, Members: members,
	}, nil
}

// resolveBlocks expands the active blocks payload plus chain layers.
func resolveBlocks(repo Repo, name string, meta Meta) (Target, error) {
	return resolveBlocksAt(repo.Dir, name, meta)
}

// resolveBlocksAt expands blocks under root/_blocks/<layer-id>/.
// Active root is the repository directory; hold root is
// _hold/<resolved-id>/. Do not call BlocksDirPath here: that always
// joins the active repo leaf.
func resolveBlocksAt(root, name string, meta Meta) (Target, error) {
	if depth := 1 + len(meta.Chain); depth > maxStackDepth {
		return Target{}, fmt.Errorf(
			"mount: %s stack holds %d members; the limit is %d",
			name, depth, maxStackDepth)
	}
	fullBlocks, blocksDir, err := resolveLayerBlocksAt(root, meta.ID, meta.Blocks)
	if err != nil {
		return Target{}, fmt.Errorf("mount: %w", err)
	}
	members := []Member{{
		ID:        meta.ID,
		Size:      meta.Size,
		MtimeUnix: meta.MtimeUnix,
		SHA256:    meta.SHA256,
		Blocks:    fullBlocks,
	}}
	for _, entry := range meta.Chain {
		if len(entry.Blocks) == 0 {
			return Target{}, fmt.Errorf(
				"mount: chain layer %s: blocks form requires a blocks manifest",
				entry.ID)
		}
		layerBlocks, _, err := resolveLayerBlocksAt(root, entry.ID, entry.Blocks)
		if err != nil {
			return Target{}, fmt.Errorf("mount: chain layer %s: %w", entry.ID, err)
		}
		members = append(members, Member{
			ID:        entry.ID,
			Size:      entry.Size,
			MtimeUnix: entry.MtimeUnix,
			SHA256:    entry.SHA256,
			Blocks:    layerBlocks,
		})
	}
	return Target{
		Library:    true,
		Name:       name,
		ID:         meta.ID,
		Format:     meta.Format.Type,
		Form:       PayloadBlocks,
		Size:       meta.Size,
		SHA256:     meta.SHA256,
		BlocksDir:  blocksDir,
		ConcatView: filepath.Join(blocksDir, fileConcatView),
		Blocks:     fullBlocks,
		Members:    members,
	}, nil
}

// resolveLayerBlocksAt checks that every manifest block of one layer
// rests in root/_blocks/<id>/ and returns the block facts with that
// directory.
func resolveLayerBlocksAt(root, id string, entries []BlockEntry) ([]BlockFile, string, error) {
	blocksDir, err := blocksDirAt(root, id)
	if err != nil {
		return nil, "", err
	}
	blocks := make([]BlockFile, 0, len(entries))
	for _, entry := range entries {
		path, err := blockPathAt(root, id, entry.Name)
		if err != nil {
			return nil, "", fmt.Errorf("block %s: %w", entry.Name, err)
		}
		if _, err := os.Stat(path); err != nil {
			return nil, "", fmt.Errorf("block %s: %w", entry.Name, err)
		}
		blocks = append(blocks, BlockFile{
			Path:      path,
			Name:      entry.Name,
			Size:      entry.Size,
			MtimeUnix: entry.MtimeUnix,
			SHA256:    entry.SHA256,
		})
	}
	return blocks, blocksDir, nil
}

func blocksDirAt(root, id string) (string, error) {
	if !onDiskID(id) {
		return "", invalidPath(id)
	}
	return filepath.Join(root, dirBlocks, id), nil
}

func blockPathAt(root, id, name string) (string, error) {
	dir, err := blocksDirAt(root, id)
	if err != nil {
		return "", err
	}
	if !isBlockName(name) {
		return "", invalidPath(name)
	}
	return filepath.Join(dir, name), nil
}

// stackMembersAt pairs every container file under root with its
// recorded facts: image.sqsh / image.dmg and _incr/<id>.sqsh.
func stackMembersAt(root string, meta Meta) ([]Member, error) {
	full := filepath.Join(root, activeImageName(meta.Format.Type))
	bytesPath, err := ResolvePayloadBytesPath(full)
	if err != nil {
		return nil, fmt.Errorf("mount: %w", err)
	}
	if _, err := os.Stat(bytesPath); err != nil {
		return nil, fmt.Errorf("mount: %w", err)
	}
	members := []Member{{
		Path:      full,
		Size:      meta.Size,
		MtimeUnix: meta.MtimeUnix,
		SHA256:    meta.SHA256,
	}}
	for _, entry := range meta.Chain {
		member, err := chainMemberAt(root, entry)
		if err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, nil
}

// chainMemberAt locates one chain layer file under root/_incr.
func chainMemberAt(root string, entry ChainEntry) (Member, error) {
	if !onDiskID(entry.ID) {
		return Member{}, fmt.Errorf("mount: chain layer %s: %w", entry.ID, invalidPath(entry.ID))
	}
	path := filepath.Join(root, dirIncr, entry.ID+squashExt)
	if _, err := os.Stat(path); err != nil {
		return Member{}, fmt.Errorf("mount: chain layer %s: %w", entry.ID, err)
	}
	return Member{
		Path:      path,
		Size:      entry.Size,
		MtimeUnix: entry.MtimeUnix,
		SHA256:    entry.SHA256,
	}, nil
}

// resolveFile handles the file-path form: the argument must name an
// existing regular file, and the single member records no facts.
func resolveFile(arg string) (Target, error) {
	abs, err := filepath.Abs(arg)
	if err != nil {
		// Only os.Getwd can fail here, which is an environment
		// fault rather than a problem with the argument shape.
		return Target{}, fmt.Errorf("mount: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Target{}, fileFormError(arg, err)
	}
	if !info.Mode().IsRegular() {
		return Target{}, fileFormError(arg,
			fmt.Errorf("%s is not a regular file", abs))
	}
	return Target{Members: []Member{{Path: abs}}}, nil
}

// fileFormError reports a file-path failure after the library-name
// path was skipped or rejected. When the argument is a relative
// slash-separated name that is not a legal library name, the error
// also names why the library form missed.
func fileFormError(arg string, err error) error {
	base := fmt.Errorf("mount: %w: %w", ErrNotImageFile, err)
	if !strings.Contains(arg, "/") || filepath.IsAbs(arg) {
		return base
	}
	_, nameErr := ParseName(arg)
	return fmt.Errorf("%w; as a library reference: %w", base, nameErr)
}
