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
	var removeOnClose bool
	cmd := &cobra.Command{
		Use:   "new [name]",
		Short: "Create an empty session",
		Long: "New creates an empty session record. Mounts join it through\n" +
			"the mount command's --session flag or the TILER_SESSION_ID\n" +
			"environment variable. Omit the name to generate an\n" +
			"adjective-animal name. A name that already exists is refused.\n" +
			"With --rm the session deletes itself when it closes: 'session\n" +
			"close' removes the record and purges the runtime of every view,\n" +
			"upper data included, instead of keeping the record for audit.",
		Args: maxOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionNew(cmd, args, removeOnClose)
		},
	}
	cmd.Flags().BoolVar(&removeOnClose, "rm", false,
		"delete the session and purge its runtime when it closes")
	return cmd
}

// runSessionNew creates one empty session: a caller-supplied name,
// or a GenerateName draw with the same collision retry mount uses.
func runSessionNew(cmd *cobra.Command, args []string, removeOnClose bool) error {
	cfg, ok := loadedConfig(cmd)
	if !ok {
		return errors.New("session new: configuration is not loaded")
	}
	engine := sessionEngine(cfg)
	var name string
	var err error
	if len(args) == 1 {
		name = args[0]
		if err := session.ValidateName(name); err != nil {
			return usageError{err}
		}
		if removeOnClose {
			err = engine.NewRemovable(name)
		} else {
			err = engine.New(name)
		}
	} else {
		name, err = allocateGeneratedSession(engine, removeOnClose, "session new")
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", name)
	return err
}

func newSessionOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open <name>",
		Short: "Restore or ensure the session's mounts",
		Long: "Open replays the session record in mount order: a healthy\n" +
			"view is skipped, a dead or missing one is cleaned and\n" +
			"remounted, so the command is idempotent and is the recovery\n" +
			"entry point after a crash. A closed session resumes the same\n" +
			"way: its views remount and the record becomes active again.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, engine, err := sessionCommandEngine(cmd, "session open", args[0])
			if err != nil {
				return err
			}
			if err := engine.Open(args[0], sessionMount(cfg, args[0])); err != nil {
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
			"upper data survives, and 'session remove' deletes it later. A\n" +
			"session created with --rm is deleted instead: its record is\n" +
			"removed and the runtime of every view is purged, upper data\n" +
			"included.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, engine, err := sessionCommandEngine(cmd, "session close", args[0])
			if err != nil {
				return err
			}
			removed, err := engine.Close(args[0], sessionUmount(cfg))
			if err != nil {
				return err
			}
			verb := "closed"
			if removed {
				verb = "removed"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", verb, args[0])
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
	var asJSON bool
	cmd := &cobra.Command{
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
			summaries := session.Summaries(records)
			if asJSON {
				return writeSessionListJSON(cmd.OutOrStdout(), summaries)
			}
			return writeSessionList(cmd.OutOrStdout(), summaries)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "render the listing as a JSON document")
	return cmd
}

func newSessionShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
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
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), rec)
			}
			return writeSessionShow(cmd.OutOrStdout(), rec)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "render the record as a JSON document")
	return cmd
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
		SessionsDir: cfg.SessionsDir,
		ImagesDir:   cfg.ImagesDir,
	}
}

// sessionMount resolves one recorded action back into an engine
// request: the image reference resolves through the library, and
// the execution shares the mount command's engine half.
func sessionMount(cfg config.Config, name string) session.MountFunc {
	return func(action session.Action) (session.Result, error) {
		target, err := image.ResolveMount(cfg, action.Image)
		if err != nil {
			return session.Result{}, fmt.Errorf("session: replay %s: %w", action.Image, err)
		}
		return runMountEngine(cfg, target, action.Mountpoint, mountFlags{
			readOnly: action.Mode == session.ModeRO,
			subdir:   action.Subdir,
		}, sessionRuntimeDir(cfg, name, action))
	}
}

// sessionUmount tears one recorded point down through the same
// engine the umount command uses. The cascade inside one view, the
// forest combination under a recorded parent, and the idempotent
// already-unmounted answer come from the engine. The combination
// directory is recovered from the mount table entry or, when the
// overlay is already gone, from the covering session record.
func sessionUmount(cfg config.Config) session.UmountFunc {
	return func(mountpoint string) error {
		table, err := loadMountTable()
		if err != nil {
			return fmt.Errorf("session: %w", err)
		}
		entries := toEntries(table)
		_, err = unmountEngine(workspace.UnmountRequest{
			Dir:        mountpoint,
			Table:      entries,
			RuntimeDir: resolveCombinationRuntime(cfg, mountpoint, entries),
			Owned:      ownedLibrarySource(cfg.ImagesDir),
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

// sessionsDocument is the session list --json envelope.
type sessionsDocument struct {
	Sessions []sessionSummaryJSON `json:"sessions"`
}

// sessionSummaryJSON is one list entry aligned with the Summary read
// model. Times are RFC3339; a zero closed_at renders as-is, matching
// the on-disk record behavior. The schema is add-only.
type sessionSummaryJSON struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Mounts    int    `json:"mounts"`
	CreatedAt string `json:"created_at"`
	ClosedAt  string `json:"closed_at"`
}

func writeSessionListJSON(w io.Writer, summaries []session.Summary) error {
	doc := sessionsDocument{Sessions: make([]sessionSummaryJSON, 0, len(summaries))}
	for _, summary := range summaries {
		doc.Sessions = append(doc.Sessions, sessionSummaryJSON{
			Name:      summary.Name,
			State:     summary.State,
			Mounts:    summary.Mounts,
			CreatedAt: summary.Created.Format(time.RFC3339),
			ClosedAt:  summary.Closed.Format(time.RFC3339),
		})
	}
	return writeJSON(w, doc)
}

// writeSessionShow renders the record header and its action log.
func writeSessionShow(w io.Writer, rec session.Record) error {
	state := rec.State
	if rec.RemoveOnClose {
		state += " rm"
	}
	header := fmt.Sprintf("%s %s created=%s",
		rec.Name, state, rec.CreatedAt.Format(time.RFC3339))
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
