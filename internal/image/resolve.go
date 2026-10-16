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

// Target is one resolved mount subject: either a library reference
// expanded into its active stack, or a single file-path image.
type Target struct {
	// Library is true for an owner/repo reference and false for a
	// file path.
	Library bool
	// Name is the owner/repo name when Library is true.
	Name string
	// ID is the full image id of the resolved active chain when
	// Library is true, and empty for a file path.
	ID string
	// Members lists the squashfs files to mount: the full image
	// first, then chain layers in stored oldest-first order.
	Members []Member
}

// Member is one squashfs file of the stack with the facts recorded in
// metadata, so callers can run the cheap quick check by default and a
// full sha256 verification on demand. A file-path member records zero
// facts because no metadata exists for it.
type Member struct {
	// Path is the full image file or an incr/<id>.sqsh layer file.
	Path string
	// Size is the recorded payload size in bytes.
	Size int64
	// MtimeUnix is the recorded modification time.
	MtimeUnix int64
	// SHA256 is the recorded content digest.
	SHA256 string
}

// ResolveMount turns a mount argument into the ordered member list a
// mount consumes. An argument that parses as a legal owner/repo name
// is always a library reference and never falls back to a file path;
// anything else is a file path that must name an existing regular
// file. Library resolution reads only the active slot; archives and
// delayed-archive holds never participate.
func ResolveMount(cfg config.Config, arg string) (Target, error) {
	if _, _, err := ParseName(arg); err == nil {
		return resolveLibrary(cfg, arg)
	}
	return resolveFile(arg)
}

// resolveLibrary expands the active metadata of one repository into
// the full image plus its chain layers in stored order.
func resolveLibrary(cfg config.Config, name string) (Target, error) {
	repo, err := OpenRepo(cfg.ImagesDir, name)
	if err != nil {
		return Target{}, fmt.Errorf("mount: %w", err)
	}
	if cfg.SplitEnabled {
		return Target{}, errors.New(
			"mount: split storage is enabled; library mounts are refused")
	}
	meta, exists, err := loadActive(repo)
	if err != nil {
		return Target{}, fmt.Errorf("mount: %w", err)
	}
	if !exists {
		return Target{}, fmt.Errorf("mount: %w %s", ErrUnknownImage, name)
	}
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
