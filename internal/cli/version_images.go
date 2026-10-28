package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

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

func writeVersionImageTable(w io.Writer, views []versionImageView) error {
	if len(views) == 0 {
		return nil
	}
	for i, view := range views {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if view.Latest == nil {
			if _, err := fmt.Fprintf(w, "%s  latest=-\n  No image versions.\n", view.Name); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%s  latest=%s  created=%s\n",
			view.Name, listID(view.Latest.ID), versionCreatedAt(view.Latest.CreatedAt)); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "  ID          KIND         CREATED               DESCRIPTION"); err != nil {
			return err
		}
		depths := make(map[string]int, len(view.Images))
		for _, node := range view.Images {
			depth := 0
			if node.Parent != "" {
				depth = depths[node.Parent] + 1
			}
			depths[node.ID] = depth
			description := image.DescriptionSummary(node.Description)
			if description == "" {
				description = "-"
			}
			if _, err := fmt.Fprintf(w, "%s%s  %-11s  %s  %s\n",
				strings.Repeat("  ", depth+1), listID(node.ID), node.Kind,
				versionCreatedAt(node.CreatedAt), description); err != nil {
				return err
			}
		}
	}
	return nil
}

func versionCreatedAt(created time.Time) string {
	return created.UTC().Format(time.RFC3339Nano)
}

func writeVersionImageDetail(w io.Writer, view versionImageView) error {
	node := view.Selected
	if node == nil {
		return fmt.Errorf("images show: %s has no image versions", view.Name)
	}
	if _, err := fmt.Fprintln(w, view.Name); err != nil {
		return err
	}
	fields := [][2]string{{"id", node.ID}, {"head", node.ID},
		{"created", node.CreatedAt.UTC().Format(time.RFC3339)},
		{"size", fmt.Sprintf("%d (%s)", node.Size, formatSize(node.Size))},
		{"sha256", node.SHA256}, {"form", versionPayloadKind(*node)},
		{"format", formatDetail(node.Format)}, {"chain", strconv.Itoa(len(view.Stack) - 1)},
		{"branches", strconv.Itoa(len(view.Images) - len(view.Stack))}, {"archives", "0"}, {"holds", "0"}}
	for _, field := range fields {
		if err := writeDetailField(w, field[0], field[1]); err != nil {
			return err
		}
	}
	for _, parent := range view.Stack[1:] {
		if _, err := fmt.Fprintf(w, "    %s  parent=%s  %s  %s\n", parent.ID,
			parent.Parent, formatSize(parent.Size), parent.CreatedAt.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}

type versionImageJSON struct {
	Name     string            `json:"name"`
	LatestID *string           `json:"latest_id"`
	Images   []image.ImageMeta `json:"images"`
}

func newVersionImageJSON(view versionImageView) versionImageJSON {
	out := versionImageJSON{Name: view.Name, Images: view.Images}
	if view.Latest != nil {
		id := view.Latest.ID
		out.LatestID = &id
	}
	return out
}

func writeVersionImagesJSON(w io.Writer, views []versionImageView) error {
	doc := struct {
		SchemaVersion string             `json:"schema_version"`
		Repositories  []versionImageJSON `json:"repositories"`
	}{SchemaVersion: image.VersionManifestVersion, Repositories: make([]versionImageJSON, 0, len(views))}
	for _, view := range views {
		doc.Repositories = append(doc.Repositories, newVersionImageJSON(view))
	}
	return writeJSON(w, doc)
}

func versionStack(view versionImageView, id string) ([]image.ImageMeta, error) {
	manifest := image.VersionManifest{Version: image.VersionManifestVersion, Name: view.Name, Images: view.Images}
	return manifest.SelectVersion(view.Name + ":" + id)
}

func versionPayloadKind(node image.ImageMeta) string {
	if len(node.Blocks) != 0 {
		return image.PayloadBlocks
	}
	return image.PayloadSingle
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
