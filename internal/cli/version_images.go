package cli

import (
	"fmt"

	"github.com/jiangxin/tiler/internal/image"
)

// versionImageView is the shared read model for the pending v3 list and show
// commands. Nodes retain complete metadata and use the forest's stable order.
type versionImageView struct {
	Name     string
	Latest   *image.ImageMeta
	Images   []image.ImageMeta
	Selected *image.ImageMeta
	Stack    []image.ImageMeta
}

func collectVersionImages(imagesDir string) ([]versionImageView, error) {
	repos, err := image.ListRepos(imagesDir)
	if err != nil {
		return nil, fmt.Errorf("images list: %w", err)
	}
	views := make([]versionImageView, 0, len(repos))
	for _, repo := range repos {
		view, err := collectVersionRepo(repo)
		if err != nil {
			return nil, fmt.Errorf("images list: %s: %w", repo.Name(), err)
		}
		views = append(views, view)
	}
	return views, nil
}

func collectVersionShow(imagesDir, selector string) (versionImageView, error) {
	name, _, err := image.ParseVersionSelector(selector)
	if err != nil {
		return versionImageView{}, err
	}
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		return versionImageView{}, err
	}
	view, err := collectVersionRepo(repo)
	if err != nil {
		return versionImageView{}, err
	}
	manifest := image.VersionManifest{
		Version: image.VersionManifestVersion,
		Name:    view.Name,
		Images:  view.Images,
	}
	view.Stack, err = manifest.SelectVersion(selector)
	if err != nil {
		return versionImageView{}, err
	}
	selected := view.Stack[len(view.Stack)-1]
	view.Selected = &selected
	return view, nil
}

func collectVersionRepo(repo image.Repo) (versionImageView, error) {
	manifest, err := image.LoadVersionManifest(repo)
	if err != nil {
		return versionImageView{}, err
	}
	forest, err := image.NewForest(manifest.Images)
	if err != nil {
		return versionImageView{}, err
	}
	view := versionImageView{Name: repo.Name(), Images: forest.Topology()}
	if latest, ok := forest.Latest(); ok {
		view.Latest = &latest
	}
	return view, nil
}
