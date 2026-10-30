package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/spf13/cobra"
)

const listIDHex = 10

func newImagesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "images", Aliases: []string{"image"}, Short: "Manage the local image library",
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	cmd.AddCommand(newImagesListCmd(), newImagesShowCmd(), newImagesDescribeCmd(), newImagesGCCmd(), newImagesRemoveCmd())
	return cmd
}

func newImagesDescribeCmd() *cobra.Command {
	var messages, files []string
	cmd := &cobra.Command{Use: "describe <name>:<hash>", Aliases: []string{"desc"},
		Short: "Update one image version description", Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !strings.Contains(args[0], ":") {
				return usageError{errors.New("description editing requires <name>:<hash>")}
			}
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images describe: configuration is not loaded")
			}
			view, err := collectVersionShow(cfg.ImagesDir, args[0])
			if err != nil {
				return fmt.Errorf("images describe: %w", err)
			}
			description, err := readDescribeDescription(cmd, messages, files, view.Selected.Description)
			if err != nil {
				return err
			}
			changed, err := image.UpdateVersionDescription(cfg.ImagesDir, view.Name, view.Selected.ID,
				view.Selected.Description, description)
			if err != nil {
				return fmt.Errorf("images describe: %w", err)
			}
			outcome := "Updated description"
			if !changed {
				outcome = "Description unchanged"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s for %s:%s.\n", outcome, view.Name, listID(view.Selected.ID))
			return err
		}}
	cmd.Flags().StringArrayVarP(&messages, "message", "m", nil, "image description paragraph (repeatable)")
	cmd.Flags().StringArrayVarP(&files, "file", "F", nil, "read image description from file (use - for stdin)")
	return cmd
}

func readDescribeDescription(cmd *cobra.Command, messages, files []string, current string) (string, error) {
	if !cmd.Flags().Changed("message") && !cmd.Flags().Changed("file") {
		return editDescription(cmd, current)
	}
	description, err := readDescriptionInput(cmd, messages, files, "images describe")
	return description.text, err
}

func newImagesListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List image versions",
		Long: "Print each repository's latest version and parent-before-child tree. Dependencies use a ↳ arrow and two spaces per level within the ID column; all columns align with spaces. SIZE is each node's own image payload size, excluding ancestors. CREATED and LAST ACTIVITY use UTC timestamps. Activity includes creation, failed actions, initialization, and descendant events propagated to ancestors, not only successful use. Missing or empty logs show -; unreadable or damaged logs show unknown with a warning on stderr. Listing does not renew activity. --json preserves full IDs and metadata without reading activity logs.",
		Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images list: configuration is not loaded")
			}
			return listAllImages(cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg, asJSON)
		}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "render the listing as a JSON document")
	return cmd
}

func newImagesShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "show <name|name:hash>", Short: "Show an image repository or version",
		Long: "Show immutable version identity, ancestry, format, payload facts, creation time, and description. A hash selector accepts an unambiguous prefix of at least four hexadecimal digits.",
		Args: exactOneArg, RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images show: configuration is not loaded")
			}
			return showOneImage(cmd.OutOrStdout(), cfg, args[0], asJSON)
		}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "render the image as a JSON document")
	return cmd
}

func listAllImages(w, warnings io.Writer, cfg config.Config, asJSON bool) error {
	views, err := collectVersionImages(cfg.ImagesDir)
	if err != nil {
		return err
	}
	if asJSON {
		return writeVersionImagesJSON(w, views)
	}
	if err := collectVersionActivity(views, cfg, warnings); err != nil {
		return err
	}
	return writeVersionImageTable(w, views)
}

func showOneImage(w io.Writer, cfg config.Config, ref string, asJSON bool) error {
	view, err := collectVersionShow(cfg.ImagesDir, ref)
	if err != nil {
		if errors.Is(err, image.ErrInvalidName) {
			return usageError{err}
		}
		return fmt.Errorf("images show: %w", err)
	}
	if asJSON {
		return writeJSON(w, newVersionImageJSON(view))
	}
	return writeVersionImageDetail(w, view)
}

func lengths(cols []string) []int {
	out := make([]int, len(cols))
	for i, col := range cols {
		out[i] = len(col)
	}
	return out
}

func writePadded(w io.Writer, cols []string, widths []int, suffix string, padLast bool) error {
	var body strings.Builder
	for i, col := range cols {
		if i > 0 {
			body.WriteString("  ")
		}
		body.WriteString(col)
		if i != len(cols)-1 || padLast {
			body.WriteString(strings.Repeat(" ", max(0, widths[i]-len(col))))
		}
	}
	if suffix != "" {
		body.WriteString("  " + suffix)
	}
	_, err := fmt.Fprintln(w, body.String())
	return err
}

func formatDetail(format image.Format) string {
	if format.Type == "" {
		return ""
	}
	if format.Compression == "" {
		return format.Type
	}
	return format.Type + "/" + format.Compression
}

func formatSize(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units, value, unit := []string{"KiB", "MiB", "GiB", "TiB"}, float64(n), -1
	for value >= 1024 && unit < len(units)-1 {
		value, unit = value/1024, unit+1
	}
	text := strings.TrimSuffix(strconv.FormatFloat(value, 'f', 1, 64), ".0")
	return text + " " + units[unit]
}

func listID(id string) string {
	if len(id) <= listIDHex {
		return id
	}
	return id[:listIDHex]
}

func writeDetailField(w io.Writer, key, value string) error {
	_, err := fmt.Fprintf(w, "  %-10s  %s\n", key, value)
	return err
}

func newImagesRemoveCmd() *cobra.Command {
	return &cobra.Command{Use: "remove <name | name:hash>", Aliases: []string{"rm"}, Short: "Remove an image repository or version subtree",
		Long: "A bare name removes the repository and all versions. A name:hash selector removes that version and its descendants. Referenced versions cannot be removed.",
		Args: exactOneArg, RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images remove: configuration is not loaded")
			}
			name, prefix, err := image.ParseVersionSelector(args[0])
			if err != nil {
				return usageError{err}
			}
			coordinator := session.ReferenceCoordinator{Store: session.Store{Dir: cfg.SessionsDir, LogsDir: cfg.LogsDir}, ImagesDir: cfg.ImagesDir}
			var removed []image.ImageMeta
			if prefix == "" {
				removed, err = coordinator.RemoveVersionRepository(name)
			} else {
				view, resolveErr := collectVersionShow(cfg.ImagesDir, args[0])
				if resolveErr != nil {
					return resolveErr
				}
				removed, err = coordinator.RemoveVersionSubtree(name, view.Selected.ID)
			}
			if err != nil {
				return versionRemovalError(cfg.SessionsDir, err)
			}
			return writeVersionRemoval(cmd.OutOrStdout(), name, prefix == "", removed)
		}}
}

func writeVersionRemoval(w io.Writer, name string, repository bool, removed []image.ImageMeta) error {
	if repository {
		_, err := fmt.Fprintf(w, "Removed image repository %s (%d image versions).\n", name, len(removed))
		return err
	}
	if _, err := fmt.Fprintf(w, "Removed %d image versions from %s:\n", len(removed), name); err != nil {
		return err
	}
	for _, node := range removed {
		if _, err := fmt.Fprintf(w, "  %s\n", listID(node.ID)); err != nil {
			return err
		}
	}
	return nil
}

func versionRemovalError(sessionsDir string, err error) error {
	var blocked *session.VersionRemovalBlocked
	if !errors.As(err, &blocked) {
		return err
	}
	type row struct{ session, tip, state string }
	rows, seen, shortIDs := []row{}, map[string]bool{}, map[string]int{}
	store := session.Store{Dir: sessionsDir}
	for _, bucket := range blocked.Blockers {
		for _, owner := range bucket.Owners {
			key := owner.Session + "\x00" + bucket.Tip
			if seen[key] {
				continue
			}
			record, loadErr := store.LoadVersion(owner.Session)
			if loadErr != nil {
				return loadErr
			}
			seen[key] = true
			rows = append(rows, row{owner.Session, bucket.Tip, record.State})
			shortIDs[listID(bucket.Tip)]++
		}
	}
	slices.SortFunc(rows, func(a, b row) int {
		if n := strings.Compare(a.session, b.session); n != 0 {
			return n
		}
		return strings.Compare(a.tip, b.tip)
	})
	var text strings.Builder
	fmt.Fprintf(&text, "%s\n", blocked)
	for _, row := range rows {
		id := listID(row.tip)
		if shortIDs[id] > 1 {
			id = row.tip
		}
		fmt.Fprintf(&text, "  %s   %s  (%s)\n", row.session, id, row.state)
	}
	text.WriteString("No image versions were removed.")
	return errors.New(text.String())
}

func isTargetUsage(err error) bool {
	return errors.Is(err, image.ErrInvalidName) || errors.Is(err, image.ErrInvalidPath) || errors.Is(err, image.ErrNameConflict)
}
