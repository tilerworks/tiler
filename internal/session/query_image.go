package session

import (
	"fmt"
	"strings"

	"github.com/jiangxin/tiler/internal/image"
)

type queryImage struct {
	name, id string
}

func (e *Engine) queryImage(selector string) (queryImage, error) {
	if selector == "" {
		return queryImage{}, nil
	}
	name, prefix, err := image.ParseVersionSelector(selector)
	filter := queryImage{name: name, id: prefix}
	if err != nil || prefix == "" {
		return filter, err
	}
	repo, err := image.OpenRepo(e.ImagesDir, name)
	if err != nil {
		return filter, err
	}
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil {
		return filter, nil
	}
	for _, node := range manifest.Images {
		if strings.HasPrefix(node.ID, prefix) {
			stack, err := manifest.SelectVersion(selector)
			if err != nil {
				return filter, err
			}
			filter.id = stack[len(stack)-1].ID
			return filter, nil
		}
	}
	filter.id = "no matching version"
	return filter, nil
}
func matchesQuery(view View, directory string, filter queryImage, imagesDir string, diagnostics *[]string) bool {
	if directory == "" && filter.name == "" {
		return true
	}
	for _, action := range view.Actions {
		if action.Type != TypeMount || directory != "" && (action.Mountpoint == "" || !relatedQueryPath(action.Mountpoint, directory)) {
			continue
		}
		if filter.name == "" {
			return true
		}
		if action.Name != filter.name {
			continue
		}
		if filter.id == "" {
			return true
		}
		served := action.Health == HealthHealthy
		stack, err := action.VersionStack(imagesDir, served)
		if err == nil {
			for _, member := range stack {
				if member.Meta.ID == filter.id {
					return true
				}
			}
			continue
		}
		*diagnostics = append(*diagnostics, fmt.Sprintf("session: %s action %d: incomplete image filter: %v", view.Name, action.Seq, err))
		root, tip := action.FullID, action.TipID
		if served && action.ServedTip != "" {
			root, tip = action.ServedID, action.ServedTip
		}
		if len(filter.id) == 64 && (filter.id == root || filter.id == tip) {
			return true
		}
	}
	return false
}
