package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jiangxin/tiler/internal/session"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status [path]",
		Short: "Reconcile sessions with the real mount state",
		Long: "Without a path, list every session record with the health of\n" +
			"its live mounts, mark the session named by TILER_SESSION_ID as\n" +
			"current, and report mounted tiler views no active record claims\n" +
			"as orphans. With a path, report the session whose recorded view\n" +
			"covers it; a path no session covers is an error. --json renders\n" +
			"one machine-readable document instead of the human listing.",
		Args: maxOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("status: configuration is not loaded")
			}
			engine := sessionEngine(cfg)
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				found, err := engine.SessionForPath(args[0])
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(out, newStatusPathDocument(found))
				}
				return writeStatusPath(out, found)
			}
			overview, err := engine.Overview(os.Getenv(sessionEnvVar))
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(out, newStatusDocument(overview))
			}
			return writeStatusOverview(out, overview)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "render the status as a JSON document")
	return cmd
}

// writeStatusOverview renders one block per session followed by the
// orphan view lines. An overview without any record or orphan says
// so explicitly.
func writeStatusOverview(w io.Writer, overview session.Overview) error {
	if len(overview.Sessions) == 0 && len(overview.Orphans) == 0 {
		_, err := fmt.Fprintln(w, "no sessions recorded")
		return err
	}
	for _, view := range overview.Sessions {
		if err := writeSessionBlock(w, view); err != nil {
			return err
		}
	}
	for _, orphan := range overview.Orphans {
		if _, err := fmt.Fprintf(w, "orphan %s %s %s\n",
			orphan.Point, orphan.FSType, orphan.RuntimeDir); err != nil {
			return err
		}
	}
	return nil
}

// writeSessionBlock renders the session header and its annotated
// action lines. A locked record reports its name alone.
func writeSessionBlock(w io.Writer, view session.View) error {
	header := view.Name
	if view.State != "" {
		header += " " + view.State
	}
	if !view.Created.IsZero() {
		header += " created=" + view.Created.Format(time.RFC3339)
	}
	if view.Current {
		header += " current"
	}
	if view.Locked {
		header += " locked"
	}
	if _, err := fmt.Fprintln(w, header); err != nil {
		return err
	}
	for _, action := range view.Actions {
		if err := writeStatusAction(w, action); err != nil {
			return err
		}
	}
	return nil
}

// writeStatusAction renders one action line; the health mark only
// appears on annotated actions.
func writeStatusAction(w io.Writer, view session.ActionView) error {
	line := fmt.Sprintf("  %d %s %s %s mode=%s status=%s",
		view.Seq, view.Type, view.Image, view.Mountpoint, view.Mode, view.Status)
	if view.Health != "" {
		line += " health=" + view.Health
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

// writeStatusPath renders the covering session of one path: the
// record header with the covering mountpoint and its stale mark,
// then the matching action line.
func writeStatusPath(w io.Writer, found session.PathSession) error {
	header := fmt.Sprintf("%s %s mountpoint=%s",
		found.Record.Name, found.Record.State, found.Mountpoint)
	if found.Stale {
		header += " stale"
	}
	if _, err := fmt.Fprintln(w, header); err != nil {
		return err
	}
	match := -1
	for i, action := range found.Record.Actions {
		if action.Type == session.TypeMount && action.Mountpoint == found.Mountpoint {
			match = i
		}
	}
	if match < 0 {
		return nil
	}
	return writeStatusAction(w, session.ActionView{Action: found.Record.Actions[match]})
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
	Name      string             `json:"name"`
	State     string             `json:"state"`
	CreatedAt string             `json:"created_at"`
	ClosedAt  string             `json:"closed_at"`
	Current   bool               `json:"current"`
	Locked    bool               `json:"locked"`
	Actions   []statusActionJSON `json:"actions"`
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

// statusPathDocument is the status <path> --json envelope.
type statusPathDocument struct {
	Path       string            `json:"path"`
	Session    string            `json:"session"`
	State      string            `json:"state"`
	Mountpoint string            `json:"mountpoint"`
	Stale      bool              `json:"stale"`
	Action     *statusActionJSON `json:"action"`
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
		Name:      view.Name,
		State:     view.State,
		CreatedAt: view.Created.Format(time.RFC3339),
		ClosedAt:  view.Closed.Format(time.RFC3339),
		Current:   view.Current,
		Locked:    view.Locked,
		Actions:   make([]statusActionJSON, 0, len(view.Actions)),
	}
	for _, action := range view.Actions {
		entry.Actions = append(entry.Actions, newStatusAction(action.Action, action.Health))
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

// newStatusPathDocument converts one reverse-lookup hit. The
// matching action carries the covering view's health, which is the
// same fact the stale flag reports; the last recorded mount of the
// point wins, matching the human form.
func newStatusPathDocument(found session.PathSession) statusPathDocument {
	doc := statusPathDocument{
		Path:       found.Path,
		Session:    found.Record.Name,
		State:      found.Record.State,
		Mountpoint: found.Mountpoint,
		Stale:      found.Stale,
	}
	health := session.HealthHealthy
	if found.Stale {
		health = session.HealthStale
	}
	for _, action := range found.Record.Actions {
		if action.Type == session.TypeMount && action.Mountpoint == found.Mountpoint {
			matched := newStatusAction(action, health)
			doc.Action = &matched
		}
	}
	return doc
}
