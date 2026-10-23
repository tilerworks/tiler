package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
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
	cmd.AddCommand(newImagesShowCmd())
	cmd.AddCommand(newImagesGCCmd())
	cmd.AddCommand(newImagesRemoveCmd())
	return cmd
}

func newImagesListCmd() *cobra.Command {
	var archives bool
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List active images",
		Long: "Print one aligned row per active image: name, id, creation\n" +
			"date, size, chain length, payload form, and the archive and\n" +
			"hold counts. The id column is a 10-hex prefix of the payload\n" +
			"sha256; that prefix locates the image when it is unique in\n" +
			"the library leaf. --archives adds those parked chain ids,\n" +
			"shortened the same way, under each row. --json keeps the\n" +
			"full sha256 in a machine-readable document. Use 'images\n" +
			"show' for one repository in detail.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images list: configuration is not loaded")
			}
			return listAllImages(cmd.OutOrStdout(), cfg, archives, asJSON)
		},
	}
	cmd.Flags().BoolVar(&archives, "archives", false, "include archive and held chain ids")
	cmd.Flags().BoolVar(&asJSON, "json", false, "render the listing as a JSON document")
	return cmd
}

func newImagesShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <name|id>",
		Short: "Show one image in detail",
		Long: "Show prints a multi-line record for one active image.\n" +
			"<name|id> is a 1-to-5 segment library name, or a full\n" +
			"payload sha256 / unique hex prefix from images list. The\n" +
			"record covers identity, the container format, each chain\n" +
			"layer, the split payload, and the parked archive and hold\n" +
			"ids. --json renders the same repository as one\n" +
			"machine-readable object.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images show: configuration is not loaded")
			}
			return showOneImage(cmd.OutOrStdout(), cfg, args[0], asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "render the image as a JSON document")
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
	return writeImageTable(w, states, archives)
}

// activeStates inspects every repository and keeps the ones with an
// active image, in listing order.
func activeStates(repos []image.Repo) ([]namedState, error) {
	states := make([]namedState, 0, len(repos))
	for _, repo := range repos {
		state, err := image.Inspect(repo)
		if err != nil {
			return nil, fmt.Errorf("images list: %s: %w", repo.Name(), err)
		}
		if !state.HasActive {
			continue
		}
		states = append(states, namedState{name: repo.Name(), state: state})
	}
	return states, nil
}

func showOneImage(w io.Writer, cfg config.Config, ref string, asJSON bool) error {
	named, err := resolveShowTarget(cfg.ImagesDir, ref)
	if err != nil {
		if isShowUsage(err) {
			return usageError{err}
		}
		return fmt.Errorf("images show: %w", err)
	}
	if asJSON {
		// Show always includes parked chains, matching the human record.
		return writeJSON(w, newImageJSON(named.name, named.state, true))
	}
	return writeImageDetail(w, named.name, named.state)
}

// resolveShowTarget accepts a library name or an id reference. A
// legal name that still has an active image wins. Otherwise a full
// sha256 or unique hex prefix locates the owning leaf.
func resolveShowTarget(imagesDir, ref string) (namedState, error) {
	_, nameErr := image.ParseName(ref)
	if nameErr == nil {
		repo, err := image.OpenRepo(imagesDir, ref)
		if err != nil {
			return namedState{}, err
		}
		state, err := image.Inspect(repo)
		if err != nil {
			return namedState{}, err
		}
		if state.HasActive {
			return namedState{name: repo.Name(), state: state}, nil
		}
		namedMiss := fmt.Errorf("%s has no active image", ref)
		named, idErr := showRepoByID(imagesDir, ref)
		if idErr == nil {
			return named, nil
		}
		if image.IsUnknownIDRef(idErr) {
			return namedState{}, namedMiss
		}
		return namedState{}, idErr
	}
	if !image.IsIDRef(ref) {
		return namedState{}, nameErr
	}
	return showRepoByID(imagesDir, ref)
}

func showRepoByID(imagesDir, ref string) (namedState, error) {
	repo, err := image.FindRepoByID(imagesDir, ref)
	if err != nil {
		return namedState{}, err
	}
	state, err := image.Inspect(repo)
	if err != nil {
		return namedState{}, err
	}
	if !state.HasActive {
		return namedState{}, fmt.Errorf("%s has no active image", repo.Name())
	}
	return namedState{name: repo.Name(), state: state}, nil
}

func isShowUsage(err error) bool {
	return errors.Is(err, image.ErrInvalidName)
}

// detailKeyWidth is the padded key column of a named image record.
// It fits the longest key ("deprecated") so optional lines do not
// shift the values.
const detailKeyWidth = 10

// imageTableHeader names the short-list columns. Widths grow to fit
// the values printed under them.
var imageTableHeader = []string{"NAME", "ID", "CREATED", "SIZE", "CHAIN", "FORM", "ARCH", "HOLD"}

// writeImageTable prints the short list: a header and one space-aligned
// row per active image. A deprecated image and a recorded ref or
// commit trail the row. With archives set, parked chain ids follow
// the row they belong to.
func writeImageTable(w io.Writer, states []namedState, archives bool) error {
	if len(states) == 0 {
		return nil
	}
	rows := make([]imageTableRow, len(states))
	widths := make([]int, len(imageTableHeader))
	copy(widths, lengths(imageTableHeader))
	padLast := false
	for i, named := range states {
		rows[i] = newImageTableRow(named)
		if rows[i].suffix != "" {
			padLast = true
		}
		for c, col := range rows[i].cols {
			if len(col) > widths[c] {
				widths[c] = len(col)
			}
		}
	}
	if err := writePadded(w, imageTableHeader, widths, "", padLast); err != nil {
		return err
	}
	for _, row := range rows {
		if err := writePadded(w, row.cols, widths, row.suffix, padLast); err != nil {
			return err
		}
		if archives {
			if err := writeSideChains(w, row.state); err != nil {
				return err
			}
		}
	}
	return nil
}

type imageTableRow struct {
	cols   []string
	suffix string
	state  image.State
}

func newImageTableRow(named namedState) imageTableRow {
	meta := named.state.Active
	var suffix []string
	if meta.Deprecated {
		suffix = append(suffix, "deprecated")
	}
	if meta.Source.Ref != "" {
		suffix = append(suffix, "ref="+meta.Source.Ref)
	}
	if meta.Source.Commit != "" {
		commit := meta.Source.Commit
		if len(commit) > 7 {
			commit = commit[:7]
		}
		suffix = append(suffix, "commit="+commit)
	}
	return imageTableRow{
		cols: []string{
			named.name,
			listID(meta.ID),
			meta.CreatedAt.UTC().Format("2006-01-02"),
			formatSize(meta.Size),
			strconv.Itoa(shownChainLength(meta)),
			image.PayloadKind(meta),
			strconv.Itoa(len(named.state.Archives)),
			strconv.Itoa(len(named.state.Holds)),
		},
		suffix: strings.Join(suffix, " "),
		state:  named.state,
	}
}

func lengths(cols []string) []int {
	out := make([]int, len(cols))
	for i, col := range cols {
		out[i] = len(col)
	}
	return out
}

// writePadded joins columns with two spaces. Every column but the
// last is padded to its width; the last is padded only when a later
// row carries a suffix, so those suffixes share a column.
func writePadded(w io.Writer, cols []string, widths []int, suffix string, padLast bool) error {
	var b strings.Builder
	for i, col := range cols {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(col)
		if i == len(cols)-1 && !padLast {
			continue
		}
		if pad := widths[i] - len(col); pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
	}
	if suffix != "" {
		b.WriteString("  ")
		b.WriteString(suffix)
	}
	_, err := fmt.Fprintln(w, b.String())
	return err
}

// writeImageDetail prints the multi-line record of one image. Parked
// chain ids are always included.
func writeImageDetail(w io.Writer, name string, state image.State) error {
	meta := state.Active
	if _, err := fmt.Fprintln(w, name); err != nil {
		return err
	}
	fields := [][2]string{
		{"id", meta.ID},
		{"created", meta.CreatedAt.UTC().Format(time.RFC3339)},
		{"size", fmt.Sprintf("%d (%s)", meta.Size, formatSize(meta.Size))},
		{"sha256", meta.SHA256},
		{"form", image.PayloadKind(meta)},
	}
	if line := formatDetail(meta.Format); line != "" {
		fields = append(fields, [2]string{"format", line})
	}
	if meta.Builder != "" {
		fields = append(fields, [2]string{"builder", meta.Builder})
	}
	if src := detailSource(meta.Source); src != "" {
		fields = append(fields, [2]string{"source", src})
	}
	if meta.Deprecated {
		fields = append(fields, [2]string{"deprecated", "true"})
	}
	fields = append(fields, [2]string{"chain", strconv.Itoa(shownChainLength(meta))})
	for _, field := range fields {
		if err := writeDetailField(w, field[0], field[1]); err != nil {
			return err
		}
	}
	for _, entry := range meta.Chain {
		if _, err := fmt.Fprintf(w, "    %s  parent=%s  %s  %s\n",
			entry.ID, entry.Parent, formatSize(entry.Size),
			entry.CreatedAt.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if image.PayloadKind(meta) == image.PayloadBlocks {
		if err := writeDetailField(w, "blocks", strconv.Itoa(len(meta.Blocks))); err != nil {
			return err
		}
	}
	if err := writeDetailField(w, "archives", strconv.Itoa(len(state.Archives))); err != nil {
		return err
	}
	for _, id := range state.Archives {
		if _, err := fmt.Fprintf(w, "    %s\n", id); err != nil {
			return err
		}
	}
	if err := writeDetailField(w, "holds", strconv.Itoa(len(state.Holds))); err != nil {
		return err
	}
	for _, id := range state.Holds {
		if _, err := fmt.Fprintf(w, "    %s\n", id); err != nil {
			return err
		}
	}
	return nil
}

func writeDetailField(w io.Writer, key, value string) error {
	_, err := fmt.Fprintf(w, "  %-*s  %s\n", detailKeyWidth, key, value)
	return err
}

func detailSource(src image.Source) string {
	var parts []string
	if src.Repo != "" {
		parts = append(parts, "repo="+src.Repo)
	}
	if src.Ref != "" {
		parts = append(parts, "ref="+src.Ref)
	}
	if src.Commit != "" {
		parts = append(parts, "commit="+src.Commit)
	}
	return strings.Join(parts, " ")
}

// shownChainLength is the chain length images list and show print.
// A dmg records an empty chain and still counts as one payload, so
// the length is the base image plus any entries. Other formats keep
// the incremental-entry count.
func shownChainLength(meta image.Meta) int {
	n := len(meta.Chain)
	if meta.Format.Type == image.FormatDMG {
		return 1 + n
	}
	return n
}

// formatDetail is the images show container line, type/compression.
func formatDetail(format image.Format) string {
	if format.Type == "" {
		return ""
	}
	if format.Compression == "" {
		return format.Type
	}
	return format.Type + "/" + format.Compression
}

// formatSize renders a byte count in binary units. Values under 1 KiB
// stay in bytes. Larger values use one decimal only when it is not
// zero.
func formatSize(n int64) string {
	if n < 0 {
		n = 0
	}
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(n)
	unit := -1
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	text := strconv.FormatFloat(value, 'f', 1, 64)
	text = strings.TrimSuffix(text, ".0")
	return text + " " + units[unit]
}

func writeSideChains(w io.Writer, state image.State) error {
	for _, id := range state.Archives {
		if _, err := fmt.Fprintf(w, "  archive %s\n", listID(id)); err != nil {
			return err
		}
	}
	for _, id := range state.Holds {
		if _, err := fmt.Fprintf(w, "  hold %s\n", listID(id)); err != nil {
			return err
		}
	}
	return nil
}

// listIDHex is how many hex digits images list prints for an id.
// The stored id stays the full sha256. A unique prefix of this
// length locates that image through resolveID.
const listIDHex = 10

func listID(id string) string {
	if len(id) <= listIDHex {
		return id
	}
	return id[:listIDHex]
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
// form field is additive: it names a blocks active and stays absent
// for the single-file default.
type metaJSON struct {
	ID         string      `json:"id"`
	Version    string      `json:"version"`
	Kind       string      `json:"kind"`
	Form       string      `json:"form,omitempty"`
	Format     string      `json:"format,omitempty"`
	Size       int64       `json:"size"`
	SHA256     string      `json:"sha256"`
	CreatedAt  string      `json:"created_at"`
	Deprecated bool        `json:"deprecated"`
	Chain      []chainJSON `json:"chain"`
}

// chainJSON is one incremental layer of the active image.
type chainJSON struct {
	ID        string `json:"id"`
	Parent    string `json:"parent"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	CreatedAt string `json:"created_at"`
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
	view.Format = formatDetail(meta.Format)
	for _, entry := range meta.Chain {
		view.Chain = append(view.Chain, chainJSON{
			ID:        entry.ID,
			Parent:    entry.Parent,
			Size:      entry.Size,
			SHA256:    entry.SHA256,
			CreatedAt: entry.CreatedAt.Format(time.RFC3339),
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
			"archives, deletes archives past the keep_versions and max_age\n" +
			"keys in .tiler.yaml, and then cleans the session records —\n" +
			"deleting closed records past the session_max_age key in\n" +
			".tiler.yaml and reporting active sessions whose mounts are\n" +
			"all stale. Active session records are never deleted\n" +
			"automatically, except a --rm one whose views are all dead:\n" +
			"it is torn down, its runtime purged, and its record removed.",
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
				if _, wErr := fmt.Fprintf(out, "%s %s %s\n",
					event.Action, event.Name, event.FullID); wErr != nil {
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
		Use:     "remove <name | name/_archives/<id>>",
		Aliases: []string{"rm"},
		Short:   "Deprecate and then remove an image target",
		Long: "The first call flags the target deprecated; the second call\n" +
			"physically removes it. <name> is a 1-to-5 segment library\n" +
			"name; an archive target is <name>/_archives/<id>, where\n" +
			"<id> is a full sha256 or a unique prefix.\n" +
			"Both calls refuse while a session still references the\n" +
			"target, and while hdiutil info shows a dmg target is\n" +
			"attached. Only image files and archives are deleted.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images remove: configuration is not loaded")
			}
			target := args[0]
			outcome, err := image.RemoveChecked(cfg, target,
				session.Refs{Dir: cfg.SessionsDir}, attachedDMG)
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
	return errors.Is(err, image.ErrInvalidName) ||
		errors.Is(err, image.ErrInvalidPath) ||
		errors.Is(err, image.ErrNameConflict)
}
