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
	// Library is true for an owner/repo reference and false for a
	// file path.
	Library bool
	// Name is the owner/repo name when Library is true.
	Name string
	// ID is the full image id of the resolved active chain or split
	// payload when Library is true, and empty for a file path.
	ID string
	// Members lists the squashfs files to stack-mount: the full
	// image first, then chain layers in stored oldest-first order.
	// These stack members are cascade layers and are unrelated to
	// the split partitions in Partitions, even though both are
	// called members in different contexts.
	Members []Member
	// Form names the split payload shape: empty for the single-file
	// cascade, or blocks / members for the two split forms.
	Form string
	// Size and SHA256 are the logical whole-image facts of a split
	// payload: the reassembled byte digest for blocks, the derived
	// manifest digest for members.
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
	// Partitions lists the member files of a members target in
	// manifest order. Split members are top-level directory
	// partitions mounted side by side, not cascade stack layers.
	Partitions []Partition
}

// BlockFile is one manifest byte block paired with its absolute
// path, so the mount layer can verify and reassemble without
// revisiting the repository layout.
type BlockFile struct {
	// Path is the absolute block file blocks/<full-id>/<name>.
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

// Partition is one split member file with its recorded facts, ready
// for the subpath forest mount and its verification.
type Partition struct {
	// Name is the member name, the top-level source directory.
	Name string
	// Path is the absolute member file members/<name>.sqsh.
	Path string
	// Size is the recorded member size in bytes.
	Size int64
	// MtimeUnix is the recorded modification time.
	MtimeUnix int64
	// SHA256 is the recorded content digest.
	SHA256 string
}

// Member is one squashfs file of the stack with the facts recorded in
// metadata, so callers can run the cheap quick check by default and a
// full sha256 verification on demand. A file-path member records zero
// facts because no metadata exists for it. For a blocks-form target,
// Path stays empty and Blocks holds the layer's byte-block files;
// mount reassembles each layer before stacking.
type Member struct {
	// Path is the full image file or an incr/<id>.sqsh layer file.
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
	// Empty for the single-file cascade and for members partitions.
	Blocks []BlockFile
}

// ResolveMount turns a mount argument into what a mount consumes:
// the ordered stack member list, or the manifest of a split payload.
// An argument that parses as a legal owner/repo name is always a
// library reference and never falls back to a file path; anything
// else is a file path that must name an existing regular file.
// Library resolution reads only the active slot and follows the
// payload kind its metadata names; archives and delayed-archive
// holds never participate.
func ResolveMount(cfg config.Config, arg string) (Target, error) {
	if _, _, err := ParseName(arg); err == nil {
		return resolveLibrary(cfg, arg)
	}
	return resolveFile(arg)
}

// resolveLibrary expands the active metadata of one repository by
// payload kind: a single-file stack, a byte-block manifest, or a
// directory-member manifest.
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
	switch PayloadKind(meta) {
	case PayloadBlocks:
		return resolveBlocks(repo, name, meta)
	case PayloadMembers:
		return resolveMembers(repo, name, meta)
	default:
		return resolveStack(repo, name, meta)
	}
}

// resolveStack expands the full image plus its chain layers in
// stored order.
func resolveStack(repo Repo, name string, meta Meta) (Target, error) {
	if depth := 1 + len(meta.Chain); depth > maxStackDepth {
		return Target{}, fmt.Errorf(
			"mount: %s stack holds %d members; the limit is %d",
			name, depth, maxStackDepth)
	}
	members, err := stackMembers(repo, meta)
	if err != nil {
		return Target{}, err
	}
	return Target{Library: true, Name: name, ID: meta.ID, Members: members}, nil
}

// resolveBlocks expands the full image blocks plus every chain layer
// that carries its own Blocks manifest into an ordered mountable
// stack (full first, then chain oldest-first). A chain entry without
// Blocks is a corrupt or unsupported layout. A missing block file
// names the entry.
func resolveBlocks(repo Repo, name string, meta Meta) (Target, error) {
	if depth := 1 + len(meta.Chain); depth > maxStackDepth {
		return Target{}, fmt.Errorf(
			"mount: %s stack holds %d members; the limit is %d",
			name, depth, maxStackDepth)
	}
	fullBlocks, blocksDir, err := resolveLayerBlocks(repo, meta.ID, meta.Blocks)
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
		layerBlocks, _, err := resolveLayerBlocks(repo, entry.ID, entry.Blocks)
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
		Form:       PayloadBlocks,
		Size:       meta.Size,
		SHA256:     meta.SHA256,
		BlocksDir:  blocksDir,
		ConcatView: filepath.Join(blocksDir, fileConcatView),
		Blocks:     fullBlocks,
		Members:    members,
	}, nil
}

// resolveLayerBlocks checks that every manifest block of one layer
// rests in blocks/<id>/ and returns the block facts with the layer
// directory.
func resolveLayerBlocks(repo Repo, id string, entries []BlockEntry) ([]BlockFile, string, error) {
	blocksDir, err := repo.BlocksDirPath(id)
	if err != nil {
		return nil, "", err
	}
	blocks := make([]BlockFile, 0, len(entries))
	for _, entry := range entries {
		path, err := repo.BlockPath(id, entry.Name)
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

// resolveMembers checks that every member file rests in members/ and
// returns the partition facts with the derived whole-image identity.
// A missing member names the entry.
func resolveMembers(repo Repo, name string, meta Meta) (Target, error) {
	partitions := make([]Partition, 0, len(meta.Members))
	for _, entry := range meta.Members {
		path, err := repo.MemberPath(entry.Name)
		if err != nil {
			return Target{}, fmt.Errorf("mount: member %s: %w", entry.Name, err)
		}
		if _, err := os.Stat(path); err != nil {
			return Target{}, fmt.Errorf("mount: member %s: %w", entry.Name, err)
		}
		partitions = append(partitions, Partition{
			Name:      entry.Name,
			Path:      path,
			Size:      entry.Size,
			MtimeUnix: entry.MtimeUnix,
			SHA256:    entry.SHA256,
		})
	}
	return Target{
		Library:    true,
		Name:       name,
		ID:         meta.ID,
		Form:       PayloadMembers,
		Size:       meta.Size,
		SHA256:     meta.SHA256,
		Partitions: partitions,
	}, nil
}

// stackMembers pairs every file of the active stack with its
// recorded facts. Every member file must exist.
func stackMembers(repo Repo, meta Meta) ([]Member, error) {
	members := []Member{{
		Path:      repo.ImagePath(),
		Size:      meta.Size,
		MtimeUnix: meta.MtimeUnix,
		SHA256:    meta.SHA256,
	}}
	for _, entry := range meta.Chain {
		member, err := chainMember(repo, entry)
		if err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, nil
}

// chainMember locates one chain layer file. The error names the
// layer id so a broken chain pinpoints the missing layer.
func chainMember(repo Repo, entry ChainEntry) (Member, error) {
	path, err := repo.IncrPath(entry.ID)
	if err != nil {
		return Member{}, fmt.Errorf("mount: chain layer %s: %w", entry.ID, err)
	}
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

// fileFormError reports a file-path failure. When the argument also
// looks like a library reference (a relative slash-separated name
// that is not legal), the error names why both forms missed.
func fileFormError(arg string, err error) error {
	base := fmt.Errorf("mount: %w: %w", ErrNotImageFile, err)
	if !strings.Contains(arg, "/") || filepath.IsAbs(arg) {
		return base
	}
	_, _, nameErr := ParseName(arg)
	return fmt.Errorf("%w; as a library reference: %w", base, nameErr)
}
