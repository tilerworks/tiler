package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
	"github.com/spf13/cobra"
)

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage mount sessions",
		Long: "A session is the durable record of an ordered set of mounts\n" +
			"and the unit of crash recovery. Every action is recorded\n" +
			"before it runs, so after a killed process or daemon one\n" +
			"'session open' replays the record and restores every mount.\n" +
			"'session close' tears the mounts down in reverse order and\n" +
			"keeps the record for audit; 'session remove' deletes a\n" +
			"closed record.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newSessionNewCmd())
	cmd.AddCommand(newSessionOpenCmd())
	cmd.AddCommand(newSessionCloseCmd())
	cmd.AddCommand(newSessionRemoveCmd())
	cmd.AddCommand(newSessionListCmd())
	cmd.AddCommand(newSessionShowCmd())
	return cmd
}

func newSessionNewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "new <name>",
		Short: "Create an empty session",
		Long: "New creates an empty session record. Mounts join it through\n" +
			"the mount command's --session flag or the TILER_SESSION_ID\n" +
			"environment variable. A name that already exists is refused.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, engine, err := sessionCommandEngine(cmd, "session new", args[0])
			if err != nil {
				return err
			}
			if err := engine.New(args[0]); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", args[0])
			return err
		},
	}
}

func newSessionOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open <name>",
		Short: "Restore or ensure the session's mounts",
		Long: "Open replays the session record in mount order: a healthy\n" +
			"view is skipped, a dead or missing one is cleaned and\n" +
			"remounted, so the command is idempotent and is the recovery\n" +
			"entry point after a crash.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, engine, err := sessionCommandEngine(cmd, "session open", args[0])
			if err != nil {
				return err
			}
			if err := engine.Open(args[0], sessionMount(cfg)); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "opened %s\n", args[0])
			return err
		},
	}
}

func newSessionCloseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "close <name>",
		Short: "Unmount everything and close the record",
		Long: "Close unmounts every recorded view in reverse mount order and\n" +
			"then marks the record closed. The record is kept for audit;\n" +
			"upper data survives, and 'session remove' deletes it later.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, engine, err := sessionCommandEngine(cmd, "session close", args[0])
			if err != nil {
				return err
			}
			if err := engine.Close(args[0], sessionUmount(cfg)); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "closed %s\n", args[0])
			return err
		},
	}
}

func newSessionRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Delete a closed session record",
		Long: "Remove deletes the record of a closed session. An active\n" +
			"session is refused: close it first, so a session that still\n" +
			"holds mounts is never orphaned.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, engine, err := sessionCommandEngine(cmd, "session remove", args[0])
			if err != nil {
				return err
			}
			if err := engine.Remove(args[0]); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
			return err
		},
	}
}

func newSessionListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every session record",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("session list: configuration is not loaded")
			}
			engine := sessionEngine(cfg)
			records, err := engine.List()
			if err != nil {
				return err
			}
			return writeSessionList(cmd.OutOrStdout(), session.Summaries(records))
		},
	}
}

func newSessionShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show one session record in detail",
		Args:  exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, engine, err := sessionCommandEngine(cmd, "session show", args[0])
			if err != nil {
				return err
			}
			rec, err := engine.Show(args[0])
			if err != nil {
				return err
			}
			return writeSessionShow(cmd.OutOrStdout(), rec)
		},
	}
}

// sessionCommandEngine loads the configuration, validates the name
// argument, and assembles the lifecycle engine. A bad name shape is
// a usage problem; everything the engine reports stays a runtime
// error.
func sessionCommandEngine(cmd *cobra.Command, label, name string) (
	config.Config, *session.Engine, error,
) {
	cfg, ok := loadedConfig(cmd)
	if !ok {
		return config.Config{}, nil, fmt.Errorf("%s: configuration is not loaded", label)
	}
	if err := session.ValidateName(name); err != nil {
		return config.Config{}, nil, usageError{err}
	}
	return cfg, sessionEngine(cfg), nil
}

// sessionEngine assembles the lifecycle engine over the loaded
// configuration. The table loads through the same seam the other
// commands use; health and clock stay the real implementations. It
// is a variable so command tests can no-op the advisory lock, the
// same seam discipline the engines follow.
var sessionEngine = func(cfg config.Config) *session.Engine {
	return &session.Engine{
		Store: session.Store{Dir: cfg.SessionsDir},
		LoadTable: func() ([]mountinfo.Mount, error) {
			return loadMountTable()
		},
		RuntimeDir: cfg.RuntimeDir,
	}
}

// sessionMount resolves one recorded action back into an engine
// request: the image reference resolves through the library, and
// the execution shares the mount command's engine half.
func sessionMount(cfg config.Config) session.MountFunc {
	return func(action session.Action) (session.Result, error) {
		target, err := image.ResolveMount(cfg, action.Image)
		if err != nil {
			return session.Result{}, fmt.Errorf("session: replay %s: %w", action.Image, err)
		}
		return runMountEngine(cfg, target, action.Mountpoint, mountFlags{
			readOnly: action.Mode == session.ModeRO,
			subdir:   action.Subdir,
		})
	}
}

// sessionUmount tears one recorded point down through the same
// engine the umount command uses. The cascade inside one view and
// the idempotent already-unmounted answer come from the engine.
func sessionUmount(cfg config.Config) session.UmountFunc {
	return func(mountpoint string) error {
		table, err := loadMountTable()
		if err != nil {
			return fmt.Errorf("session: %w", err)
		}
		_, err = unmountEngine(workspace.UnmountRequest{
			Dir:        mountpoint,
			Table:      toEntries(table),
			RuntimeDir: cfg.RuntimeDir,
			Reload:     reloadEntries,
			Run:        mountRun,
		})
		return err
	}
}

// writeSessionList renders one line per record.
func writeSessionList(w io.Writer, summaries []session.Summary) error {
	for _, summary := range summaries {
		line := fmt.Sprintf("%s %s mounts=%d", summary.Name, summary.State, summary.Mounts)
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// writeSessionShow renders the record header and its action log.
func writeSessionShow(w io.Writer, rec session.Record) error {
	header := fmt.Sprintf("%s %s created=%s",
		rec.Name, rec.State, rec.CreatedAt.Format(time.RFC3339))
	if !rec.ClosedAt.IsZero() {
		header += " closed=" + rec.ClosedAt.Format(time.RFC3339)
	}
	if _, err := fmt.Fprintln(w, header); err != nil {
		return err
	}
	for _, action := range rec.Actions {
		line := fmt.Sprintf("  %d %s %s %s mode=%s status=%s at=%s",
			action.Seq, action.Type, action.Image, action.Mountpoint,
			action.Mode, action.Status, action.Timestamp.Format(time.RFC3339))
		if action.Subdir != "" {
			line += " subdir=" + action.Subdir
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}
