package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jiangxin/tiler/internal/session"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var asJSON bool
	var selectedSession, selectedImage string
	cmd := &cobra.Command{
		Use:     "status [directory]",
		Aliases: []string{"st"},
		Short:   "Reconcile sessions with the real mount state",
		Long: "List sessions and their layered mounts, marking the current session\n" +
			"and reporting unclaimed views as orphans. A directory selects all\n" +
			"sessions with mounts above or below it. Use --session for an exact\n" +
			"session or --image name[:version] for any layer of an image.\n" +
			"Filters intersect; selected sessions retain all their mounts.\n" +
			"--json always renders an overview document with a sessions array.",
		Args: maxOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("status: configuration is not loaded")
			}
			engine := sessionEngine(cfg)
			query := session.Query{Session: selectedSession, Image: selectedImage}
			if len(args) == 1 {
				query.Directory = args[0]
			}
			if selectedSession != "" {
				if err := session.ValidateName(selectedSession); err != nil {
					return usageError{err}
				}
			}
			return runStatusQuery(cmd, engine, query, asJSON, true)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "render the status as a JSON document")
	cmd.Flags().StringVarP(&selectedSession, "session", "s", "", "select an exact session name")
	cmd.Flags().StringVarP(&selectedImage, "image", "i", "", "select any layer of name[:version]")
	return cmd
}

func runStatusQuery(cmd *cobra.Command, engine *session.Engine, query session.Query, asJSON, includeOrphans bool) error {
	overview, err := engine.Query(os.Getenv(sessionEnvVar), query)
	if err != nil {
		return err
	}
	for _, diagnostic := range overview.Diagnostics {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), terminalText(diagnostic)); err != nil {
			return err
		}
	}
	if !includeOrphans {
		overview.Orphans = nil
	}
	loadStatusActivity(engine, &overview, cmd.ErrOrStderr())
	if asJSON {
		return writeJSON(cmd.OutOrStdout(), newStatusDocument(overview))
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return writeStatusDetails(cmd.OutOrStdout(), overview, sessionTimeNow(), cwd)
}

// statusDocument is the status --json overview envelope. The schema
// is add-only; golden files pin the shape.
type statusDocument struct {
	CurrentSession string              `json:"current_session"`
	Sessions       []statusSessionJSON `json:"sessions"`
	OrphanViews    []statusOrphanJSON  `json:"orphan_views"`
}

// statusSessionJSON is one session block of the overview document.
type statusSessionJSON struct {
	sessionActivityJSON
	Name      string             `json:"name"`
	State     string             `json:"state"`
	CreatedAt string             `json:"created_at"`
	ClosedAt  string             `json:"closed_at"`
	Current   bool               `json:"current"`
	Locked    bool               `json:"locked"`
	Actions   []statusActionJSON `json:"actions"`
	Mounts    []statusMountJSON  `json:"mounts"`
}

// statusActionJSON is one recorded action with its health mark.
// Times are RFC3339; zero times render as-is, matching the on-disk
// record behavior.
type statusActionJSON struct {
	Seq        int    `json:"seq"`
	Type       string `json:"type"`
	Image      string `json:"image"`
	Mountpoint string `json:"mountpoint"`
	Mode       string `json:"mode"`
	Subdir     string `json:"subdir"`
	Status     string `json:"status"`
	Health     string `json:"health"`
	Timestamp  string `json:"timestamp"`
}

// statusOrphanJSON is one unclaimed mounted view.
type statusOrphanJSON struct {
	Point      string `json:"point"`
	FSType     string `json:"fstype"`
	RuntimeDir string `json:"runtime_dir"`
}

func newStatusDocument(overview session.Overview) statusDocument {
	doc := statusDocument{
		CurrentSession: overview.Current,
		Sessions:       make([]statusSessionJSON, 0, len(overview.Sessions)),
		OrphanViews:    make([]statusOrphanJSON, 0, len(overview.Orphans)),
	}
	for _, view := range overview.Sessions {
		doc.Sessions = append(doc.Sessions, newStatusSession(view))
	}
	for _, orphan := range overview.Orphans {
		doc.OrphanViews = append(doc.OrphanViews, statusOrphanJSON{
			Point:      orphan.Point,
			FSType:     orphan.FSType,
			RuntimeDir: orphan.RuntimeDir,
		})
	}
	return doc
}

func newStatusSession(view session.View) statusSessionJSON {
	entry := statusSessionJSON{
		sessionActivityJSON: newSessionActivityJSON(view.Activity),
		Name:                view.Name,
		State:               view.State,
		CreatedAt:           view.Created.Format(time.RFC3339),
		ClosedAt:            view.Closed.Format(time.RFC3339),
		Current:             view.Current,
		Locked:              view.Locked,
		Actions:             make([]statusActionJSON, 0, len(view.Actions)),
		Mounts:              make([]statusMountJSON, 0, len(view.Mounts)),
	}
	for _, action := range view.Actions {
		entry.Actions = append(entry.Actions, newStatusAction(action.Action, action.Health))
	}
	for _, mount := range view.Mounts {
		entry.Mounts = append(entry.Mounts, newStatusMount(mount))
	}
	return entry
}

func newStatusAction(action session.Action, health string) statusActionJSON {
	return statusActionJSON{
		Seq:        action.Seq,
		Type:       action.Type,
		Image:      action.Image,
		Mountpoint: action.Mountpoint,
		Mode:       action.Mode,
		Subdir:     action.Subdir,
		Status:     action.Status,
		Health:     health,
		Timestamp:  action.Timestamp.Format(time.RFC3339),
	}
}

type statusMountJSON struct {
	statusActionJSON
	FullID        string            `json:"full_id"`
	TipID         string            `json:"tip_id"`
	ServedID      string            `json:"served_id"`
	ServedTip     string            `json:"served_tip"`
	Layers        []statusLayerJSON `json:"layers"`
	OverlayUpper  string            `json:"overlay_upper"`
	OverlayLowers []string          `json:"overlay_lowers"`
	DiskImage     string            `json:"disk_image"`
	Shadow        string            `json:"shadow"`
	Diagnostics   []string          `json:"diagnostics"`
}

type statusLayerJSON struct {
	Image      string `json:"image"`
	Version    string `json:"version"`
	Kind       string `json:"kind"`
	Mountpoint string `json:"mountpoint"`
	Available  bool   `json:"available"`
}

func newStatusMount(mount session.MountView) statusMountJSON {
	entry := statusMountJSON{
		statusActionJSON: newStatusAction(mount.Action, mount.Health),
		FullID:           mount.FullID, TipID: mount.TipID, ServedID: mount.ServedID, ServedTip: mount.ServedTip,
		Layers: make([]statusLayerJSON, 0, len(mount.Layers)), OverlayUpper: mount.OverlayUpper,
		OverlayLowers: append([]string{}, mount.OverlayLowers...), DiskImage: mount.DiskImage,
		Shadow: mount.Shadow, Diagnostics: append([]string{}, mount.Diagnostics...),
	}
	for _, layer := range mount.Layers {
		entry.Layers = append(entry.Layers, statusLayerJSON(layer))
	}
	return entry
}
