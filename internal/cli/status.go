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
	return &cobra.Command{
		Use:   "status [path]",
		Short: "Reconcile sessions with the real mount state",
		Long: "Without a path, list every session record with the health of\n" +
			"its live mounts, mark the session named by TILER_SESSION_ID as\n" +
			"current, and report mounted tiler views no active record claims\n" +
			"as orphans. With a path, report the session whose recorded view\n" +
			"covers it; a path no session covers is an error.",
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
				return writeStatusPath(out, found)
			}
			overview, err := engine.Overview(os.Getenv(sessionEnvVar))
			if err != nil {
				return err
			}
			return writeStatusOverview(out, overview)
		},
	}
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
