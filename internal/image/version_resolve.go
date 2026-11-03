package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
)

// ResolveVersionMount resolves v3 selectors while preserving file precedence.
func ResolveVersionMount(cfg config.Config, arg string) (Target, error) {
	clean := filepath.Clean(arg)
	if info, err := os.Stat(clean); err == nil && info.Mode().IsRegular() {
		if target, owned, err := resolveObjectOwner(cfg.ImagesDir, clean); owned || err != nil {
			return target, err
		}
		return resolveFile(clean)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Target{}, fmt.Errorf("mount: %w", err)
	}
	name, _, err := ParseVersionSelector(arg)
	if err != nil {
		return resolveFile(arg)
	}
	repo, err := OpenRepo(cfg.ImagesDir, name)
	if err != nil {
		return Target{}, fmt.Errorf("mount: %w", err)
	}
	return resolveVersionRepo(repo, arg)
}

func resolveVersionRepo(repo Repo, selector string) (Target, error) {
	manifest, err := LoadVersionManifest(repo)
	if err != nil {
		return Target{}, fmt.Errorf("mount: %w", err)
	}
	stack, err := manifest.SelectVersion(selector)
	if err != nil {
		return Target{}, err
	}
	members := make([]Member, 0, len(stack))
	for _, node := range stack {
		member, err := resolveVersionMember(repo, node)
		if err != nil {
			return Target{}, fmt.Errorf("mount: image %q version %s: %w", repo.Name(), node.ID, err)
		}
		members = append(members, member)
	}
	tip := stack[len(stack)-1]
	return Target{Library: true, Name: repo.Name(), ID: stack[0].ID, Tip: tip.ID,
		Format: tip.Format.Type, Members: members}, nil
}

func resolveVersionMember(repo Repo, node ImageMeta) (Member, error) {
	member := Member{ID: node.ID, Format: node.Format.Type, Size: node.Size,
		MtimeUnix: node.MtimeUnix, SHA256: node.SHA256}
	if len(node.Blocks) == 0 {
		path, err := repo.ObjectPayloadPath(node)
		if err != nil {
			return Member{}, err
		}
		if _, err := os.Stat(path); err != nil {
			return Member{}, err
		}
		member.Path = path
		return member, nil
	}
	for _, block := range node.Blocks {
		path, err := repo.ObjectBlockPath(node.ID, block.Name)
		if err != nil {
			return Member{}, err
		}
		if _, err := statBlockPayload(BlockFile{Path: path, Name: block.Name}); err != nil {
			return Member{}, err
		}
		member.Blocks = append(member.Blocks, BlockFile{Path: path, Name: block.Name, Size: block.Size,
			MtimeUnix: block.MtimeUnix, SHA256: block.SHA256})
	}
	return member, nil
}

func resolveObjectOwner(imagesDir, path string) (Target, bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Target{}, false, err
	}
	rel, err := filepath.Rel(imagesDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Target{}, false, nil
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	i := len(parts) - 3
	if i < 1 || parts[i] != dirObjects {
		return Target{}, false, nil
	}
	repo, err := OpenRepo(imagesDir, strings.Join(parts[:i], "/"))
	if err != nil {
		return Target{}, true, err
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil {
		return Target{}, true, fmt.Errorf("mount: %w", err)
	}
	for _, node := range manifest.Images {
		leaf := parts[i+2]
		owned := leaf == activeImageName(node.Format.Type)
		if len(node.Blocks) != 0 {
			owned = leaf == fileConcatView || slices.ContainsFunc(node.Blocks,
				func(block BlockEntry) bool { return block.Name == leaf })
		}
		if node.ID != parts[i+1] || !owned {
			continue
		}
		target, err := resolveVersionRepo(repo, repo.Name()+":"+node.ID)
		return target, true, err
	}
	return Target{}, true, fmt.Errorf("mount: immutable object has no metadata owner: %s", abs)
}
