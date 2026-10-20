package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/spf13/cobra"
)

func newImagesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "images",
		Short: "Manage the local image library",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newImagesListCmd())
	cmd.AddCommand(newImagesGCCmd())
	cmd.AddCommand(newImagesRemoveCmd())
	return cmd
}

func newImagesListCmd() *cobra.Command {
	var archives bool
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list [owner/repo]",
		Aliases: []string{"ls"},
		Short:   "List active images",
		Long: "Without a name, list every active image in the library. With a\n" +
			"name, also list each incremental chain id. --archives adds the\n" +
			"archive and held chain ids. --json renders one machine-readable\n" +
			"document instead of the human listing.",
		Args: maxOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images list: configuration is not loaded")
			}
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				return listOneImage(out, cfg, args[0], archives, asJSON)
			}
			return listAllImages(out, cfg, archives, asJSON)
		},
	}
	cmd.Flags().BoolVar(&archives, "archives", false, "include archive and held chain ids")
	cmd.Flags().BoolVar(&asJSON, "json", false, "render the listing as a JSON document")
	return cmd
}

// namedState pairs a canonical repository name with its listing
// state. Both output forms render the same collected states.
type namedState struct {
	name  string
	state image.State
}

func listAllImages(w io.Writer, cfg config.Config, archives, asJSON bool) error {
	repos, err := image.ListRepos(cfg.ImagesDir)
	if err != nil {
		return fmt.Errorf("images list: %w", err)
	}
	states, err := activeStates(repos)
	if err != nil {
		return err
	}
	if asJSON {
		return writeImagesJSON(w, states, archives)
	}
	for _, named := range states {
		if err := writeImageLine(w, named.name, named.state); err != nil {
			return err
		}
		if archives {
			if err := writeSideChains(w, named.state); err != nil {
				return err
			}
		}
	}
	return nil
}

// activeStates inspects every repository and keeps the ones with an
// active image, in listing order.
func activeStates(repos []image.Repo) ([]namedState, error) {
	states := make([]namedState, 0, len(repos))
	for _, repo := range repos {
		state, err := image.Inspect(repo)
		if err != nil {
			return nil, fmt.Errorf("images list: %s/%s: %w", repo.Owner, repo.Repo, err)
		}
		if !state.HasActive {
			continue
		}
		states = append(states, namedState{name: repo.Owner + "/" + repo.Repo, state: state})
	}
	return states, nil
}

func listOneImage(w io.Writer, cfg config.Config, name string, archives, asJSON bool) error {
	repo, err := image.OpenRepo(cfg.ImagesDir, name)
	if err != nil {
		return usageError{err}
	}
	state, err := image.Inspect(repo)
	if err != nil {
		return fmt.Errorf("images list: %w", err)
	}
	if !state.HasActive {
		return fmt.Errorf("images list: %s has no active image", name)
	}
	if asJSON {
		named := namedState{name: repo.Owner + "/" + repo.Repo, state: state}
		return writeImagesJSON(w, []namedState{named}, archives)
	}
	if err := writeImageLine(w, name, state); err != nil {
		return err
	}
	for _, entry := range state.Active.Chain {
		if _, err := fmt.Fprintf(w, "  incr %s\n", entry.ID); err != nil {
			return err
		}
	}
	// The single-name listing expands the split payload manifest:
	// every partition of a members form, the block count of a
	// blocks form. The single-file form keeps its chain listing.
	switch image.PayloadKind(state.Active) {
	case image.PayloadMembers:
		for _, entry := range state.Active.Members {
			if _, err := fmt.Fprintf(w, "  member %s\n", entry.Name); err != nil {
				return err
			}
		}
	case image.PayloadBlocks:
		if _, err := fmt.Fprintf(w, "  blocks %d\n", len(state.Active.Blocks)); err != nil {
			return err
		}
	}
	if archives {
		return writeSideChains(w, state)
	}
	return nil
}

func writeImageLine(w io.Writer, name string, state image.State) error {
	line := fmt.Sprintf("%s %s chain=%d", name, state.Active.ID, len(state.Active.Chain))
	if state.Active.Deprecated {
		line += " deprecated"
	}
	// The form marker is appended for split actives only, so the
	// default single-file output stays byte-identical.
	if kind := image.PayloadKind(state.Active); kind != image.PayloadSingle {
		line += " form=" + kind
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

func writeSideChains(w io.Writer, state image.State) error {
	for _, id := range state.Archives {
		if _, err := fmt.Fprintf(w, "  archive %s\n", id); err != nil {
			return err
		}
	}
	for _, id := range state.Holds {
		if _, err := fmt.Fprintf(w, "  hold %s\n", id); err != nil {
			return err
		}
	}
	return nil
}

// imagesDocument is the images list --json envelope: one object
// holding every listed repository.
type imagesDocument struct {
	Images []imageJSON `json:"images"`
}

// imageJSON is one repository in the machine-readable listing. The
// schema is add-only; golden files pin the shape.
type imageJSON struct {
	Name     string   `json:"name"`
	Active   metaJSON `json:"active"`
	Archives []string `json:"archives"`
	Holds    []string `json:"holds"`
}

// metaJSON is the active image of one repository. image.Meta stays
// yaml-only; this view is the JSON contract. Times are RFC3339. The
// split form fields are additive: form names a split active and
// stays absent for the single-file default, members lists the
// partition manifest of a members form.
type metaJSON struct {
	ID         string       `json:"id"`
	Version    string       `json:"version"`
	Kind       string       `json:"kind"`
	Form       string       `json:"form,omitempty"`
	Size       int64        `json:"size"`
	SHA256     string       `json:"sha256"`
	CreatedAt  string       `json:"created_at"`
	Deprecated bool         `json:"deprecated"`
	Chain      []chainJSON  `json:"chain"`
	Members    []memberJSON `json:"members,omitempty"`
}

// chainJSON is one incremental layer of the active image.
type chainJSON struct {
	ID        string `json:"id"`
	Parent    string `json:"parent"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	CreatedAt string `json:"created_at"`
}

// memberJSON is one partition of a members-form split payload.
type memberJSON struct {
	Name   string `json:"name"`
	Image  string `json:"image"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func writeImagesJSON(w io.Writer, states []namedState, archives bool) error {
	doc := imagesDocument{Images: make([]imageJSON, 0, len(states))}
	for _, named := range states {
		doc.Images = append(doc.Images, newImageJSON(named.name, named.state, archives))
	}
	return writeJSON(w, doc)
}

// newImageJSON converts one repository state. Without the archives
// flag the side chains stay empty arrays, mirroring the human form.
func newImageJSON(name string, state image.State, archives bool) imageJSON {
	view := imageJSON{
		Name:     name,
		Active:   newMetaJSON(state.Active),
		Archives: []string{},
		Holds:    []string{},
	}
	if archives {
		if state.Archives != nil {
			view.Archives = state.Archives
		}
		if state.Holds != nil {
			view.Holds = state.Holds
		}
	}
	return view
}

func newMetaJSON(meta image.Meta) metaJSON {
	view := metaJSON{
		ID:         meta.ID,
		Version:    meta.Version,
		Kind:       meta.Kind,
		Size:       meta.Size,
		SHA256:     meta.SHA256,
		CreatedAt:  meta.CreatedAt.Format(time.RFC3339),
		Deprecated: meta.Deprecated,
		Chain:      make([]chainJSON, 0, len(meta.Chain)),
	}
	if kind := image.PayloadKind(meta); kind != image.PayloadSingle {
		view.Form = kind
	}
	for _, entry := range meta.Chain {
		view.Chain = append(view.Chain, chainJSON{
			ID:        entry.ID,
			Parent:    entry.Parent,
			Size:      entry.Size,
			SHA256:    entry.SHA256,
			CreatedAt: entry.CreatedAt.Format(time.RFC3339),
		})
	}
	for _, entry := range meta.Members {
		view.Members = append(view.Members, memberJSON{
			Name:   entry.Name,
			Image:  entry.Image,
			Size:   entry.Size,
			SHA256: entry.SHA256,
		})
	}
	return view
}

func newImagesGCCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gc",
		Short: "Promote held chains, expire archives, and clean session records",
		Long: "One garbage collection entry manages two kinds of expired\n" +
			"objects: it promotes every unreferenced image hold into\n" +
			"archives, deletes the archives past the retention rules, and\n" +
			"then cleans the session records — deleting closed records past\n" +
			"the session age limit and reporting active sessions whose\n" +
			"mounts are all stale. Active session records are never\n" +
			"deleted automatically, except a --rm one whose views are all\n" +
			"dead: it is torn down, its runtime purged, and its record\n" +
			"removed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images gc: configuration is not loaded")
			}
			now := time.Now()
			refs := session.Refs{Dir: cfg.SessionsDir}
			events, gcErr := image.GC(cfg, refs, now)
			out := cmd.OutOrStdout()
			for _, event := range events {
				if _, wErr := fmt.Fprintf(out, "%s %s/%s %s\n",
					event.Action, event.Owner, event.Repo, event.FullID); wErr != nil {
					return wErr
				}
			}
			cleaned, cleanErr := sessionEngine(cfg).Cleanup(
				cfg.SessionMaxAge, cfg.SessionAutocloseStale, now, sessionUmount(cfg))
			for _, event := range cleaned {
				if _, wErr := fmt.Fprintf(out, "%s session %s\n",
					event.Action, event.Session); wErr != nil {
					return wErr
				}
			}
			if err := errors.Join(gcErr, cleanErr); err != nil {
				return fmt.Errorf("images gc: %w", err)
			}
			return nil
		},
	}
}

func newImagesRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <owner/repo | owner/repo/archives/full-id>",
		Aliases: []string{"rm"},
		Short:   "Deprecate and then remove an image target",
		Long: "The first call flags the target deprecated; the second call\n" +
			"physically removes it. Both calls refuse while a session still\n" +
			"references the target.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images remove: configuration is not loaded")
			}
			target := args[0]
			outcome, err := image.RemoveTarget(cfg, target,
				session.Refs{Dir: cfg.SessionsDir})
			if err != nil {
				if isTargetUsage(err) {
					return usageError{err}
				}
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", outcome, target)
			return err
		},
	}
}

// isTargetUsage reports whether err rejects the target shape itself,
// which is a command-line usage problem, not a runtime failure.
func isTargetUsage(err error) bool {
	return errors.Is(err, image.ErrInvalidName) || errors.Is(err, image.ErrInvalidPath)
}
