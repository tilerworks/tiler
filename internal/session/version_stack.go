package session

import (
	"fmt"

	"github.com/jiangxin/tiler/internal/image"
)

// VersionMember pairs immutable node facts with its container or concat path.
type VersionMember struct {
	Meta image.ImageMeta
	Path string
}

// VersionStack resolves a library mount's recorded desired or served identity.
// An absent served override means the desired stack is being served. File mounts
// use their separate absolute-path/digest flow. Callers hold the library lock
// while coordinating recovery, resolving this snapshot and preparing its payloads.
func (a Action) VersionStack(imagesDir string, served bool) ([]VersionMember, error) {
	if err := a.validateVersionIdentity(); err != nil {
		return nil, fmt.Errorf("session: action %d: %w", a.Seq, err)
	}
	if a.Type != TypeMount || a.Name == "" {
		return nil, fmt.Errorf("session: action %d: a library mount is required", a.Seq)
	}
	root, tip := a.FullID, a.TipID
	if served && a.ServedTip != "" {
		root, tip = a.ServedID, a.ServedTip
	}
	repo, err := image.OpenRepo(imagesDir, a.Name)
	if err != nil {
		return nil, err
	}
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil {
		return nil, fmt.Errorf("session: load %s: %w", repo.MetaPath(), err)
	}
	stack, err := manifest.SelectVersion(a.Name + ":" + tip)
	if err != nil {
		return nil, err
	}
	if stack[0].ID != root {
		return nil, fmt.Errorf("session: action %d: recorded root %s does not match tip %s", a.Seq, root, tip)
	}
	members := make([]VersionMember, len(stack))
	for i, node := range stack {
		path, err := repo.ObjectPayloadPath(node)
		if err != nil {
			return nil, err
		}
		members[i] = VersionMember{Meta: node, Path: path}
	}
	return members, nil
}
